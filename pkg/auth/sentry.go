// Package auth provides authentication and authorization for the MCP server.
package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
)

// DaprSentryAuthenticator validates JWT tokens issued by Dapr Sentry.
type DaprSentryAuthenticator struct {
	config      DaprSentryConfig
	jwks        *jose.JSONWebKeySet
	jwksMu      sync.RWMutex
	lastRefresh time.Time
	httpClient  *http.Client
	logger      *slog.Logger
}

// daprSentryClaims represents the JWT claims from Dapr Sentry tokens.
type daprSentryClaims struct {
	jwt.Claims
	Use string `json:"use,omitempty"`
}

// NewDaprSentryAuthenticator creates a new Dapr Sentry authenticator.
func NewDaprSentryAuthenticator(ctx context.Context, cfg DaprSentryConfig) (*DaprSentryAuthenticator, error) {
	return NewDaprSentryAuthenticatorWithLogger(ctx, cfg, nil)
}

// NewDaprSentryAuthenticatorWithLogger creates a new Dapr Sentry authenticator with a custom logger.
func NewDaprSentryAuthenticatorWithLogger(ctx context.Context, cfg DaprSentryConfig, logger *slog.Logger) (*DaprSentryAuthenticator, error) {
	if logger == nil {
		logger = slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelDebug}))
	}

	logger.Debug("[SENTRY-AUTH] creating authenticator",
		"jwks_url", cfg.JWKSUrl,
		"trust_domain", cfg.TrustDomain,
		"audience", cfg.Audience,
		"token_header", cfg.TokenHeader,
		"refresh_interval", cfg.RefreshInterval,
	)

	a := &DaprSentryAuthenticator{
		config: cfg,
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
		},
		logger: logger,
	}

	// Initial JWKS fetch with timeout
	fetchCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	logger.Debug("[SENTRY-AUTH] fetching initial JWKS",
		"url", cfg.JWKSUrl,
	)

	if err := a.refreshJWKS(fetchCtx); err != nil {
		logger.Error("[SENTRY-AUTH] failed to fetch initial JWKS",
			"url", cfg.JWKSUrl,
			"error", err,
		)
		return nil, fmt.Errorf("failed to fetch JWKS from %s: %w", cfg.JWKSUrl, err)
	}

	a.jwksMu.RLock()
	keyCount := 0
	if a.jwks != nil {
		keyCount = len(a.jwks.Keys)
	}
	a.jwksMu.RUnlock()

	logger.Debug("[SENTRY-AUTH] authenticator initialized",
		"jwks_key_count", keyCount,
	)

	return a, nil
}

// Mode returns the authentication mode.
func (a *DaprSentryAuthenticator) Mode() AuthMode {
	return ModeDaprSentry
}

// Authenticate validates a Dapr Sentry JWT token and returns the identity.
func (a *DaprSentryAuthenticator) Authenticate(ctx context.Context, token string) (*Identity, error) {
	a.logger.Debug("[SENTRY-AUTH] starting authentication",
		"token_length", len(token),
	)

	// Parse the JWT without verification to get headers
	parsedJWT, err := jwt.ParseSigned(token, []jose.SignatureAlgorithm{
		jose.RS256, jose.RS384, jose.RS512,
		jose.ES256, jose.ES384, jose.ES512,
		jose.PS256, jose.PS384, jose.PS512,
		jose.EdDSA,
	})
	if err != nil {
		a.logger.Debug("[SENTRY-AUTH] failed to parse JWT",
			"error", err,
			"token_preview", safeTokenPreview(token),
		)
		return nil, fmt.Errorf("%w: failed to parse JWT: %v", ErrInvalidToken, err)
	}

	if len(parsedJWT.Headers) == 0 {
		a.logger.Debug("[SENTRY-AUTH] JWT has no headers")
		return nil, fmt.Errorf("%w: no JWT headers", ErrInvalidToken)
	}

	header := parsedJWT.Headers[0]
	a.logger.Debug("[SENTRY-AUTH] JWT parsed successfully",
		"algorithm", header.Algorithm,
		"key_id", header.KeyID,
		"header_count", len(parsedJWT.Headers),
	)

	// Get the signing key from JWKS
	key, err := a.getSigningKey(ctx, header.KeyID)
	if err != nil {
		a.logger.Debug("[SENTRY-AUTH] failed to get signing key",
			"key_id", header.KeyID,
			"error", err,
		)
		return nil, fmt.Errorf("%w: %v", ErrInvalidToken, err)
	}

	a.logger.Debug("[SENTRY-AUTH] signing key retrieved",
		"key_id", header.KeyID,
	)

	// Verify signature and extract claims
	var claims daprSentryClaims
	if err := parsedJWT.Claims(key, &claims); err != nil {
		a.logger.Debug("[SENTRY-AUTH] failed to verify JWT signature",
			"error", err,
		)
		return nil, fmt.Errorf("%w: failed to verify JWT signature: %v", ErrInvalidToken, err)
	}

	a.logger.Debug("[SENTRY-AUTH] JWT signature verified, validating claims",
		"subject", claims.Subject,
		"audience", claims.Audience,
		"issuer", claims.Issuer,
		"jti", claims.ID,
	)

	// Validate time-based claims
	now := time.Now()

	if claims.Expiry != nil {
		a.logger.Debug("[SENTRY-AUTH] checking expiry",
			"expiry", claims.Expiry.Time(),
			"now", now,
			"expired", now.After(claims.Expiry.Time()),
		)
		if now.After(claims.Expiry.Time()) {
			a.logger.Debug("[SENTRY-AUTH] token expired",
				"expiry", claims.Expiry.Time(),
				"now", now,
			)
			return nil, ErrTokenExpired
		}
	}

	if claims.NotBefore != nil {
		a.logger.Debug("[SENTRY-AUTH] checking not-before",
			"not_before", claims.NotBefore.Time(),
			"now", now,
			"not_yet_valid", now.Before(claims.NotBefore.Time()),
		)
		if now.Before(claims.NotBefore.Time()) {
			a.logger.Debug("[SENTRY-AUTH] token not yet valid",
				"not_before", claims.NotBefore.Time(),
				"now", now,
			)
			return nil, fmt.Errorf("%w: token not yet valid", ErrInvalidToken)
		}
	}

	// Validate audience if configured
	if a.config.Audience != "" {
		a.logger.Debug("[SENTRY-AUTH] checking audience",
			"expected", a.config.Audience,
			"actual", claims.Audience,
		)
		if !containsAudience(claims.Audience, a.config.Audience) {
			a.logger.Debug("[SENTRY-AUTH] audience mismatch",
				"expected", a.config.Audience,
				"actual", claims.Audience,
			)
			return nil, fmt.Errorf("%w: expected audience %q", ErrInvalidAudience, a.config.Audience)
		}
	} else {
		a.logger.Debug("[SENTRY-AUTH] skipping audience validation (not configured)")
	}

	// Validate SPIFFE ID in subject claim
	subject := claims.Subject
	if subject == "" {
		a.logger.Debug("[SENTRY-AUTH] missing subject claim")
		return nil, fmt.Errorf("%w: missing subject claim", ErrInvalidToken)
	}

	// Validate trust domain in SPIFFE ID
	a.logger.Debug("[SENTRY-AUTH] validating trust domain",
		"subject", subject,
		"expected_trust_domain", a.config.TrustDomain,
	)
	if !isValidSPIFFEID(subject, a.config.TrustDomain) {
		a.logger.Debug("[SENTRY-AUTH] trust domain validation failed",
			"subject", subject,
			"expected_trust_domain", a.config.TrustDomain,
		)
		return nil, fmt.Errorf("%w: invalid trust domain in subject %q, expected %q",
			ErrInvalidToken, subject, a.config.TrustDomain)
	}

	// Build identity
	identity := &Identity{
		Subject:    subject,
		Audience:   claims.Audience,
		AuthMethod: ModeDaprSentry,
		Claims:     make(map[string]interface{}),
	}

	// Add claims to identity
	if claims.ID != "" {
		identity.Claims["jti"] = claims.ID
	}
	if claims.Use != "" {
		identity.Claims["use"] = claims.Use
	}
	if claims.IssuedAt != nil {
		identity.Claims["iat"] = claims.IssuedAt.Time().Unix()
	}
	if claims.Expiry != nil {
		identity.Claims["exp"] = claims.Expiry.Time().Unix()
	}

	a.logger.Debug("[SENTRY-AUTH] authentication successful",
		"subject", identity.Subject,
		"audience", identity.Audience,
	)

	return identity, nil
}

// safeTokenPreview returns a safe preview of the token for debugging.
func safeTokenPreview(token string) string {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return fmt.Sprintf("[invalid JWT format: %d parts]", len(parts))
	}
	return fmt.Sprintf("header=%d chars, payload=%d chars, sig=%d chars",
		len(parts[0]), len(parts[1]), len(parts[2]))
}

// getSigningKey retrieves the signing key from the JWKS, refreshing if necessary.
func (a *DaprSentryAuthenticator) getSigningKey(ctx context.Context, keyID string) (interface{}, error) {
	a.jwksMu.RLock()
	jwks := a.jwks
	lastRefresh := a.lastRefresh
	a.jwksMu.RUnlock()

	a.logger.Debug("[SENTRY-AUTH] getting signing key",
		"requested_key_id", keyID,
		"last_refresh", lastRefresh,
		"refresh_interval", a.config.RefreshInterval,
		"time_since_refresh", time.Since(lastRefresh),
	)

	// Check if refresh is needed
	if time.Since(lastRefresh) > a.config.RefreshInterval {
		a.logger.Debug("[SENTRY-AUTH] JWKS cache expired, refreshing")
		if err := a.refreshJWKS(ctx); err != nil {
			a.logger.Debug("[SENTRY-AUTH] JWKS refresh failed, using cached keys",
				"error", err,
			)
		}
		a.jwksMu.RLock()
		jwks = a.jwks
		a.jwksMu.RUnlock()
	}

	if jwks == nil {
		a.logger.Debug("[SENTRY-AUTH] no JWKS available")
		return nil, fmt.Errorf("no JWKS available")
	}

	// Log available keys
	var availableKeyIDs []string
	for _, k := range jwks.Keys {
		availableKeyIDs = append(availableKeyIDs, k.KeyID)
	}
	a.logger.Debug("[SENTRY-AUTH] available keys in JWKS",
		"key_ids", availableKeyIDs,
		"total_keys", len(jwks.Keys),
	)

	// Find the key by ID
	var matchingKeys []jose.JSONWebKey
	if keyID != "" {
		matchingKeys = jwks.Key(keyID)
		a.logger.Debug("[SENTRY-AUTH] searching for specific key",
			"key_id", keyID,
			"matches_found", len(matchingKeys),
		)
	} else {
		// If no key ID, use all keys
		matchingKeys = jwks.Keys
		a.logger.Debug("[SENTRY-AUTH] no key ID specified, using all keys",
			"key_count", len(matchingKeys),
		)
	}

	if len(matchingKeys) == 0 {
		a.logger.Debug("[SENTRY-AUTH] key not found, refreshing JWKS",
			"key_id", keyID,
		)
		// Key not found, try refreshing JWKS
		if err := a.refreshJWKS(ctx); err != nil {
			a.logger.Debug("[SENTRY-AUTH] JWKS refresh failed",
				"error", err,
			)
			return nil, fmt.Errorf("key %q not found and refresh failed: %w", keyID, err)
		}

		a.jwksMu.RLock()
		jwks = a.jwks
		a.jwksMu.RUnlock()

		if keyID != "" {
			matchingKeys = jwks.Key(keyID)
		} else {
			matchingKeys = jwks.Keys
		}

		if len(matchingKeys) == 0 {
			a.logger.Debug("[SENTRY-AUTH] key still not found after refresh",
				"key_id", keyID,
				"available_keys", availableKeyIDs,
			)
			return nil, fmt.Errorf("key %q not found in JWKS", keyID)
		}
	}

	a.logger.Debug("[SENTRY-AUTH] key found",
		"key_id", matchingKeys[0].KeyID,
		"algorithm", matchingKeys[0].Algorithm,
		"use", matchingKeys[0].Use,
	)

	// Return the first matching key
	return matchingKeys[0].Key, nil
}

// refreshJWKS fetches the JWKS from the configured URL.
func (a *DaprSentryAuthenticator) refreshJWKS(ctx context.Context) error {
	a.logger.Debug("[SENTRY-AUTH] refreshing JWKS",
		"url", a.config.JWKSUrl,
	)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.config.JWKSUrl, nil) //nolint:gosec // URL is from trusted server config, not user input
	if err != nil {
		a.logger.Debug("[SENTRY-AUTH] failed to create JWKS request",
			"error", err,
		)
		return fmt.Errorf("failed to create request: %w", err)
	}

	resp, err := a.httpClient.Do(req) //nolint:gosec // URL is from trusted server config, not user input
	if err != nil {
		a.logger.Debug("[SENTRY-AUTH] JWKS HTTP request failed",
			"error", err,
		)
		return fmt.Errorf("failed to fetch JWKS: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	a.logger.Debug("[SENTRY-AUTH] JWKS response received",
		"status_code", resp.StatusCode,
		"content_type", resp.Header.Get("Content-Type"),
	)

	if resp.StatusCode != http.StatusOK {
		a.logger.Debug("[SENTRY-AUTH] JWKS endpoint returned non-OK status",
			"status_code", resp.StatusCode,
		)
		return fmt.Errorf("JWKS endpoint returned status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		a.logger.Debug("[SENTRY-AUTH] failed to read JWKS response body",
			"error", err,
		)
		return fmt.Errorf("failed to read JWKS response: %w", err)
	}

	a.logger.Debug("[SENTRY-AUTH] JWKS response body",
		"body_length", len(body),
		"body_preview", string(body[:min(200, len(body))]),
	)

	var jwks jose.JSONWebKeySet
	if err := json.Unmarshal(body, &jwks); err != nil {
		a.logger.Debug("[SENTRY-AUTH] failed to parse JWKS JSON",
			"error", err,
			"body", string(body),
		)
		return fmt.Errorf("failed to parse JWKS: %w", err)
	}

	a.logger.Debug("[SENTRY-AUTH] JWKS parsed successfully",
		"key_count", len(jwks.Keys),
	)

	for i, key := range jwks.Keys {
		a.logger.Debug("[SENTRY-AUTH] JWKS key",
			"index", i,
			"key_id", key.KeyID,
			"algorithm", key.Algorithm,
			"use", key.Use,
		)
	}

	a.jwksMu.Lock()
	a.jwks = &jwks
	a.lastRefresh = time.Now()
	a.jwksMu.Unlock()

	a.logger.Debug("[SENTRY-AUTH] JWKS cache updated",
		"key_count", len(jwks.Keys),
	)

	return nil
}

// containsAudience checks if the audience list contains the expected audience.
func containsAudience(audiences jwt.Audience, expected string) bool {
	for _, aud := range audiences {
		if aud == expected {
			return true
		}
	}
	return false
}

// isValidSPIFFEID validates that the subject is a valid SPIFFE ID with the expected trust domain.
func isValidSPIFFEID(subject, trustDomain string) bool {
	// SPIFFE ID format: spiffe://<trust-domain>/<path>
	if !strings.HasPrefix(subject, "spiffe://") {
		return false
	}

	// Extract trust domain from SPIFFE ID
	withoutScheme := strings.TrimPrefix(subject, "spiffe://")
	parts := strings.SplitN(withoutScheme, "/", 2)
	if len(parts) == 0 {
		return false
	}

	return parts[0] == trustDomain
}
