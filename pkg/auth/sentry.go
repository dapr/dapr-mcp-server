// Package auth provides authentication and authorization for the MCP server.
package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
	"github.com/spiffe/go-spiffe/v2/spiffeid"
)

const (
	// jwksHTTPTimeout bounds a single JWKS fetch.
	jwksHTTPTimeout = 10 * time.Second
	// initialJWKSFetchTimeout bounds the JWKS fetch made by the constructor.
	initialJWKSFetchTimeout = 30 * time.Second
	// maxJWKSResponseBytes caps the JWKS response body.
	maxJWKSResponseBytes = 1 << 20
	// maxJWKSStaleness is how long cached keys stay usable without a successful refresh.
	maxJWKSStaleness = 24 * time.Hour
	// clockSkewLeeway is the tolerance applied to exp, nbf and iat.
	clockSkewLeeway = time.Minute
	// jwkUseSignature is the JWK "use" value for signing keys.
	jwkUseSignature = "sig"
)

// sentrySigningAlgorithms are the asymmetric algorithms accepted on Sentry tokens.
// Symmetric algorithms and "none" are rejected while parsing.
var sentrySigningAlgorithms = []jose.SignatureAlgorithm{
	jose.RS256, jose.RS384, jose.RS512,
	jose.ES256, jose.ES384, jose.ES512,
	jose.PS256, jose.PS384, jose.PS512,
	jose.EdDSA,
}

// Errors returned while fetching or using the Dapr Sentry JWKS.
var (
	// ErrJWKSUnavailable is returned when no usable JWKS is cached.
	ErrJWKSUnavailable = errors.New("no JWKS available")
	// ErrJWKSStale is returned when cached keys are older than the maximum staleness
	// because Sentry could not be reached.
	ErrJWKSStale = errors.New("cached JWKS is stale")
	// ErrSigningKeyNotFound is returned when no JWKS key matches the token header.
	ErrSigningKeyNotFound = errors.New("signing key not found in JWKS")
)

// DaprSentryAuthenticator validates JWT tokens issued by Dapr Sentry.
type DaprSentryAuthenticator struct {
	config      DaprSentryConfig
	trustDomain spiffeid.TrustDomain
	httpClient  *http.Client
	logger      *slog.Logger
	now         func() time.Time

	// refreshMu serializes JWKS fetches so concurrent callers share one fetch.
	refreshMu sync.Mutex

	jwksMu      sync.RWMutex
	jwks        *jose.JSONWebKeySet
	lastRefresh time.Time // last successful fetch
	lastAttempt time.Time // last fetch attempt, successful or not
}

// daprSentryClaims represents the JWT claims from Dapr Sentry tokens.
type daprSentryClaims struct {
	jwt.Claims
	Use string `json:"use,omitempty"`
}

// NewDaprSentryAuthenticator creates a new Dapr Sentry authenticator that logs to slog.Default.
func NewDaprSentryAuthenticator(ctx context.Context, cfg DaprSentryConfig) (*DaprSentryAuthenticator, error) {
	return NewDaprSentryAuthenticatorWithLogger(ctx, cfg, nil)
}

// NewDaprSentryAuthenticatorWithLogger creates a new Dapr Sentry authenticator with a custom logger.
// A nil logger means slog.Default.
// The JWKS is fetched once before returning.
func NewDaprSentryAuthenticatorWithLogger(ctx context.Context, cfg DaprSentryConfig, logger *slog.Logger) (*DaprSentryAuthenticator, error) {
	if logger == nil {
		logger = slog.Default()
	}
	if cfg.RefreshInterval == 0 {
		cfg.RefreshInterval = DefaultJWKSRefreshInterval
	}
	if err := cfg.validateSettings(); err != nil {
		return nil, err
	}
	td, err := spiffeid.TrustDomainFromString(cfg.TrustDomain)
	if err != nil {
		return nil, fmt.Errorf("parse sentry trust domain %q: %w", cfg.TrustDomain, err)
	}

	a := &DaprSentryAuthenticator{
		config:      cfg,
		trustDomain: td,
		httpClient:  &http.Client{Timeout: jwksHTTPTimeout},
		logger:      logger,
		now:         time.Now,
	}

	fetchCtx, cancel := context.WithTimeout(ctx, initialJWKSFetchTimeout)
	defer cancel()
	if err := a.refreshJWKS(fetchCtx); err != nil {
		return nil, fmt.Errorf("fetch JWKS from %s: %w", cfg.JWKSUrl, err)
	}

	logger.Debug("sentry authenticator initialized",
		"jwks_url", cfg.JWKSUrl,
		"trust_domain", cfg.TrustDomain,
		"audience", cfg.Audience,
		"issuer", cfg.Issuer,
		"refresh_interval", cfg.RefreshInterval,
	)
	return a, nil
}

// Mode returns the authentication mode.
func (a *DaprSentryAuthenticator) Mode() AuthMode {
	return ModeDaprSentry
}

// Close releases idle JWKS connections.
func (a *DaprSentryAuthenticator) Close() error {
	a.httpClient.CloseIdleConnections()
	return nil
}

// Authenticate validates a Dapr Sentry JWT token and returns the identity.
func (a *DaprSentryAuthenticator) Authenticate(ctx context.Context, token string) (*Identity, error) {
	parsedJWT, err := jwt.ParseSigned(token, sentrySigningAlgorithms)
	if err != nil {
		a.logger.Debug("sentry token parse failed", "error", err, "token_shape", safeTokenPreview(token))
		return nil, fmt.Errorf("%w: parse JWT: %w", ErrInvalidToken, err)
	}
	if len(parsedJWT.Headers) == 0 {
		return nil, fmt.Errorf("%w: no JWT headers", ErrInvalidToken)
	}
	header := parsedJWT.Headers[0]

	keys, err := a.signingKeys(ctx, header.KeyID, header.Algorithm)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidToken, err)
	}

	claims, err := verifyWithAnyKey(parsedJWT, keys)
	if err != nil {
		return nil, fmt.Errorf("%w: verify JWT signature: %w", ErrInvalidToken, err)
	}

	if err := a.validateClaims(claims); err != nil {
		a.logger.Debug("sentry token claims rejected", "error", err, "subject", claims.Subject)
		return nil, err
	}

	identity := &Identity{
		Subject:    claims.Subject,
		Issuer:     claims.Issuer,
		Audience:   claims.Audience,
		AuthMethod: ModeDaprSentry,
		Claims:     make(map[string]interface{}),
	}
	if claims.ID != "" {
		identity.Claims["jti"] = claims.ID
	}
	if claims.Use != "" {
		identity.Claims["use"] = claims.Use
	}
	if claims.IssuedAt != nil {
		identity.Claims["iat"] = claims.IssuedAt.Time().Unix()
	}
	identity.Claims["exp"] = claims.Expiry.Time().Unix()

	return identity, nil
}

// verifyWithAnyKey returns the claims verified by the first key whose signature matches.
func verifyWithAnyKey(token *jwt.JSONWebToken, keys []jose.JSONWebKey) (*daprSentryClaims, error) {
	errs := make([]error, 0, len(keys))
	for _, k := range keys {
		var claims daprSentryClaims
		err := token.Claims(k.Key, &claims)
		if err == nil {
			return &claims, nil
		}
		errs = append(errs, err)
	}
	return nil, errors.Join(errs...)
}

// validateClaims checks the time-based claims, issuer, audience and SPIFFE subject.
func (a *DaprSentryAuthenticator) validateClaims(claims *daprSentryClaims) error {
	if claims.Expiry == nil {
		return fmt.Errorf("%w: missing exp claim", ErrInvalidToken)
	}

	expected := jwt.Expected{
		Issuer:      a.config.Issuer,
		AnyAudience: jwt.Audience{a.config.Audience},
		Time:        a.now(),
	}
	if err := claims.ValidateWithLeeway(expected, clockSkewLeeway); err != nil {
		switch {
		case errors.Is(err, jwt.ErrExpired):
			return fmt.Errorf("%w: %w", ErrTokenExpired, err)
		case errors.Is(err, jwt.ErrInvalidIssuer):
			return fmt.Errorf("%w: expected issuer %q", ErrInvalidIssuer, a.config.Issuer)
		case errors.Is(err, jwt.ErrInvalidAudience):
			return fmt.Errorf("%w: expected audience %q", ErrInvalidAudience, a.config.Audience)
		default:
			return fmt.Errorf("%w: %w", ErrInvalidToken, err)
		}
	}

	if _, err := parseSPIFFESubject(claims.Subject, a.trustDomain); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidToken, err)
	}
	return nil
}

// parseSPIFFESubject parses subject as a workload SPIFFE ID in trust domain td.
func parseSPIFFESubject(subject string, td spiffeid.TrustDomain) (spiffeid.ID, error) {
	if subject == "" {
		return spiffeid.ID{}, errors.New("missing subject claim")
	}
	id, err := spiffeid.FromString(subject)
	if err != nil {
		return spiffeid.ID{}, fmt.Errorf("subject %q is not a SPIFFE ID: %w", subject, err)
	}
	if !id.MemberOf(td) {
		return spiffeid.ID{}, fmt.Errorf("subject %q is not in trust domain %q", subject, td.Name())
	}
	if id.Path() == "" {
		return spiffeid.ID{}, fmt.Errorf("subject %q has no workload path", subject)
	}
	return id, nil
}

// safeTokenPreview describes the shape of a token without revealing its contents.
func safeTokenPreview(token string) string {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return fmt.Sprintf("[invalid JWT format: %d parts]", len(parts))
	}
	return fmt.Sprintf("header=%d chars, payload=%d chars, sig=%d chars",
		len(parts[0]), len(parts[1]), len(parts[2]))
}

// jwksState is a snapshot of the cached key set and its refresh times.
type jwksState struct {
	jwks        *jose.JSONWebKeySet
	lastRefresh time.Time
	lastAttempt time.Time
}

func (a *DaprSentryAuthenticator) snapshot() jwksState {
	a.jwksMu.RLock()
	defer a.jwksMu.RUnlock()
	return jwksState{jwks: a.jwks, lastRefresh: a.lastRefresh, lastAttempt: a.lastAttempt}
}

// cooledDown reports whether enough time has passed since the last fetch attempt to try again.
func (a *DaprSentryAuthenticator) cooledDown(s jwksState) bool {
	return a.now().Sub(s.lastAttempt) >= MinJWKSRefreshInterval
}

// signingKeys returns the cached keys that may have signed a token with the given kid and alg.
// It refreshes the JWKS when the cache is due, or when no key matches,
// at most once per MinJWKSRefreshInterval.
func (a *DaprSentryAuthenticator) signingKeys(ctx context.Context, kid, alg string) ([]jose.JSONWebKey, error) {
	if s := a.snapshot(); a.now().Sub(s.lastRefresh) > a.config.RefreshInterval && a.cooledDown(s) {
		// While the cached keys are still usable, only one caller pays for the scheduled fetch,
		// and the rest carry on with the cache instead of queuing behind it.
		wait := a.usable(s) != nil
		a.refreshIfDue(ctx, wait, func(s jwksState) bool {
			return a.now().Sub(s.lastRefresh) > a.config.RefreshInterval
		})
	}

	s := a.snapshot()
	if err := a.usable(s); err != nil {
		return nil, err
	}
	if keys := candidateKeys(s.jwks, kid, alg); len(keys) > 0 {
		return keys, nil
	}

	if a.cooledDown(s) {
		a.refreshIfDue(ctx, true, func(jwksState) bool { return true })
		s = a.snapshot()
		if err := a.usable(s); err != nil {
			return nil, err
		}
		if keys := candidateKeys(s.jwks, kid, alg); len(keys) > 0 {
			return keys, nil
		}
	}
	return nil, fmt.Errorf("%w: kid %q, alg %q", ErrSigningKeyNotFound, kid, alg)
}

func (a *DaprSentryAuthenticator) usable(s jwksState) error {
	if s.jwks == nil {
		return ErrJWKSUnavailable
	}
	if age := a.now().Sub(s.lastRefresh); age > maxJWKSStaleness {
		return fmt.Errorf("%w: last refreshed %s ago", ErrJWKSStale, age.Round(time.Second))
	}
	return nil
}

// refreshIfDue fetches the JWKS unless another caller fetched it within the cooldown
// while this one waited, or need reports the fetch is no longer required.
// When wait is false and another fetch is already in flight, it returns at once.
// Failures are logged; callers fall back to the cached keys.
func (a *DaprSentryAuthenticator) refreshIfDue(ctx context.Context, wait bool, need func(jwksState) bool) {
	if wait {
		a.refreshMu.Lock()
	} else if !a.refreshMu.TryLock() {
		return
	}
	defer a.refreshMu.Unlock()

	s := a.snapshot()
	if !a.cooledDown(s) || !need(s) {
		return
	}
	// A caller giving up must not fail the shared fetch and start a cooldown for everyone else.
	if err := a.refreshJWKS(context.WithoutCancel(ctx)); err != nil {
		a.logger.Warn("sentry JWKS refresh failed, using cached keys", "url", a.config.JWKSUrl, "error", err)
	}
}

// candidateKeys returns the signing keys in set that match kid (any key when kid is empty)
// and are compatible with the token's alg.
func candidateKeys(set *jose.JSONWebKeySet, kid, alg string) []jose.JSONWebKey {
	if set == nil {
		return nil
	}
	keys := set.Keys
	if kid != "" {
		keys = set.Key(kid)
	}
	out := make([]jose.JSONWebKey, 0, len(keys))
	for _, k := range keys {
		if k.Algorithm != "" && k.Algorithm != alg {
			continue
		}
		if k.Use != "" && k.Use != jwkUseSignature {
			continue
		}
		out = append(out, k)
	}
	return out
}

// refreshJWKS fetches the JWKS from the configured URL and records the attempt.
func (a *DaprSentryAuthenticator) refreshJWKS(ctx context.Context) error {
	jwks, err := a.fetchJWKS(ctx)

	a.jwksMu.Lock()
	defer a.jwksMu.Unlock()
	a.lastAttempt = a.now()
	if err != nil {
		return err
	}
	a.jwks = jwks
	a.lastRefresh = a.lastAttempt
	return nil
}

func (a *DaprSentryAuthenticator) fetchJWKS(ctx context.Context) (*jose.JSONWebKeySet, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.config.JWKSUrl, nil) //nolint:gosec // URL is from trusted server config, not user input
	if err != nil {
		return nil, fmt.Errorf("create JWKS request: %w", err)
	}

	resp, err := a.httpClient.Do(req) //nolint:gosec // URL is from trusted server config, not user input
	if err != nil {
		return nil, fmt.Errorf("fetch JWKS: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("JWKS endpoint returned status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxJWKSResponseBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read JWKS response: %w", err)
	}
	if len(body) > maxJWKSResponseBytes {
		return nil, fmt.Errorf("JWKS response exceeds %d bytes", maxJWKSResponseBytes)
	}

	var jwks jose.JSONWebKeySet
	if err := json.Unmarshal(body, &jwks); err != nil {
		return nil, fmt.Errorf("parse JWKS: %w", err)
	}
	if len(jwks.Keys) == 0 {
		return nil, errors.New("JWKS contains no keys")
	}

	a.logger.Debug("sentry JWKS refreshed", "key_count", len(jwks.Keys))
	return &jwks, nil
}
