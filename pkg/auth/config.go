// Package auth provides authentication and authorization for the MCP server.
package auth

import (
	"errors"
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Environment variables read by DefaultConfig.
const (
	envAuthEnabled           = "AUTH_ENABLED" // removed; still read to reject it
	envAuthMode              = "AUTH_MODE"
	envAuthSkipPaths         = "AUTH_SKIP_PATHS" // removed; still read to reject it
	envOIDCEnabled           = "OIDC_ENABLED"
	envOIDCIssuerURL         = "OIDC_ISSUER_URL"
	envOIDCClientID          = "OIDC_CLIENT_ID"
	envOIDCAllowedAlgorithms = "OIDC_ALLOWED_ALGORITHMS"
	envOIDCSkipIssuerCheck   = "OIDC_SKIP_ISSUER_CHECK"
	envSPIFFEEnabled         = "SPIFFE_ENABLED"
	envSPIFFETrustDomain     = "SPIFFE_TRUST_DOMAIN"
	envSPIFFEServerID        = "SPIFFE_SERVER_ID"
	envSPIFFEEndpointSocket  = "SPIFFE_ENDPOINT_SOCKET"
	envSPIFFEAllowedClients  = "SPIFFE_ALLOWED_CLIENTS"
	envSentryEnabled         = "DAPR_SENTRY_ENABLED"
	envSentryJWKSURL         = "DAPR_SENTRY_JWKS_URL"
	envSentryTrustDomain     = "DAPR_SENTRY_TRUST_DOMAIN"
	envSentryAudience        = "DAPR_SENTRY_AUDIENCE"
	envSentryIssuer          = "DAPR_SENTRY_ISSUER"
	envSentryTokenHeader     = "DAPR_SENTRY_TOKEN_HEADER" //nolint:gosec // environment variable name, not a credential
	envSentryRefreshInterval = "DAPR_SENTRY_JWKS_REFRESH_INTERVAL"
)

const (
	// DefaultJWKSRefreshInterval is how often the Dapr Sentry JWKS is refreshed when not configured.
	DefaultJWKSRefreshInterval = 5 * time.Minute
	// MinJWKSRefreshInterval is the shortest allowed JWKS refresh interval.
	// It is also the cooldown between JWKS fetch attempts, successful or not.
	MinJWKSRefreshInterval = 30 * time.Second

	authorizationHeader = "Authorization"
	listSeparator       = ","
)

var (
	defaultOIDCAlgorithms    = []string{"RS256", "ES256"}
	defaultSentryTokenHeader = authorizationHeader
)

// Configuration errors returned by the Validate methods.
var (
	// ErrAuthEnabledRemoved is returned when the removed AUTH_ENABLED variable is set.
	ErrAuthEnabledRemoved = errors.New("AUTH_ENABLED is no longer supported; AUTH_MODE alone turns authentication on (unset or \"disabled\" turns it off)")
	// ErrAuthSkipPathsRemoved is returned when the removed AUTH_SKIP_PATHS variable is set.
	ErrAuthSkipPathsRemoved = errors.New("AUTH_SKIP_PATHS is no longer supported; health endpoints never require authentication and every other path, including the MCP endpoint, always does")
	// ErrOIDCIssuerURLRequired is returned when OIDC is enabled without OIDC_ISSUER_URL.
	ErrOIDCIssuerURLRequired = errors.New("OIDC_ISSUER_URL is required")
	// ErrOIDCClientIDRequired is returned when OIDC is enabled without OIDC_CLIENT_ID.
	ErrOIDCClientIDRequired = errors.New("OIDC_CLIENT_ID is required")
	// ErrSPIFFETrustDomainRequired is returned when SPIFFE is enabled without SPIFFE_TRUST_DOMAIN.
	ErrSPIFFETrustDomainRequired = errors.New("SPIFFE_TRUST_DOMAIN is required")
	// ErrSPIFFEServerIDRequired is returned when SPIFFE is enabled without SPIFFE_SERVER_ID.
	ErrSPIFFEServerIDRequired = errors.New("SPIFFE_SERVER_ID is required")
	// ErrSentryJWKSURLRequired is returned when Dapr Sentry is enabled without DAPR_SENTRY_JWKS_URL.
	ErrSentryJWKSURLRequired = errors.New("DAPR_SENTRY_JWKS_URL is required")
	// ErrSentryTrustDomainRequired is returned when Dapr Sentry is enabled without DAPR_SENTRY_TRUST_DOMAIN.
	ErrSentryTrustDomainRequired = errors.New("DAPR_SENTRY_TRUST_DOMAIN is required")
	// ErrSentryAudienceRequired is returned when Dapr Sentry is enabled without DAPR_SENTRY_AUDIENCE.
	ErrSentryAudienceRequired = errors.New("DAPR_SENTRY_AUDIENCE is required: set it to the audience your Sentry tokens are issued for")
	// ErrSentryRefreshInterval is returned when DAPR_SENTRY_JWKS_REFRESH_INTERVAL is invalid or too short.
	ErrSentryRefreshInterval = errors.New("invalid DAPR_SENTRY_JWKS_REFRESH_INTERVAL")
)

// Config holds the authentication configuration.
type Config struct {
	// Mode is the authentication mode.
	// Only ModeDisabled turns authentication off; any other value, including
	// the empty string of a zero-value Config, requires authentication and
	// fails Validate unless it names a supported mode.
	// DefaultConfig maps an unset AUTH_MODE to ModeDisabled.
	Mode AuthMode

	// OIDC configuration
	OIDC OIDCConfig

	// SPIFFE configuration
	SPIFFE SPIFFEConfig

	// DaprSentry configuration
	DaprSentry DaprSentryConfig

	// envErr records environment variables DefaultConfig could not parse.
	// Validate reports it.
	envErr error
}

// Enabled reports whether authentication is required.
// It is true for every mode other than ModeDisabled, so an unrecognized mode fails closed.
func (c Config) Enabled() bool {
	return c.Mode != ModeDisabled
}

// OIDCConfig holds OIDC-specific configuration.
type OIDCConfig struct {
	// Enabled determines if OIDC authentication is enabled.
	Enabled bool
	// IssuerURL is the OIDC provider URL.
	IssuerURL string
	// ClientID is the expected audience (aud claim).
	ClientID string
	// AllowedAlgorithms are the allowed signing algorithms.
	AllowedAlgorithms []string
	// SkipIssuerCheck skips issuer validation (dev only).
	SkipIssuerCheck bool
}

// SPIFFEConfig holds SPIFFE-specific configuration.
type SPIFFEConfig struct {
	// Enabled determines if SPIFFE authentication is enabled.
	Enabled bool
	// TrustDomain is the SPIFFE trust domain.
	TrustDomain string
	// ServerID is this server's SPIFFE ID.
	ServerID string
	// EndpointSocket is the Workload API socket path.
	EndpointSocket string
	// AllowedClients are the allowed client SPIFFE IDs.
	AllowedClients []string
}

// DaprSentryConfig holds Dapr Sentry JWT-specific configuration.
type DaprSentryConfig struct {
	// Enabled determines if Dapr Sentry authentication is enabled.
	Enabled bool
	// JWKSUrl is the URL to fetch JWKS from Dapr Sentry.
	JWKSUrl string
	// TrustDomain is the expected trust domain in SPIFFE IDs.
	TrustDomain string
	// Audience is the expected audience claim. It is required.
	Audience string
	// Issuer is the expected iss claim. It is checked only when set.
	Issuer string
	// TokenHeader is the custom header to extract the token from (default: Authorization).
	TokenHeader string
	// RefreshInterval is the interval for refreshing the JWKS cache.
	// Zero means DefaultJWKSRefreshInterval.
	RefreshInterval time.Duration
}

// DefaultConfig returns configuration from environment variables.
// Values that cannot be parsed are reported by Validate.
func DefaultConfig() Config {
	var removedErr error
	if os.Getenv(envAuthEnabled) != "" {
		removedErr = ErrAuthEnabledRemoved
	}
	mode := AuthMode(strings.ToLower(strings.TrimSpace(os.Getenv(envAuthMode))))
	if mode == "" {
		mode = ModeDisabled
	}

	var skipPathsErr error
	if os.Getenv(envAuthSkipPaths) != "" {
		skipPathsErr = ErrAuthSkipPathsRemoved
	}

	oidcConfig, oidcErr := defaultOIDCConfig()
	spiffeConfig, spiffeErr := defaultSPIFFEConfig()
	daprSentryConfig, sentryErr := defaultDaprSentryConfig()

	// Single-mode auth enables its method without the redundant *_ENABLED flag.
	// Hybrid mode keeps the individual *_ENABLED flags.
	switch mode {
	case ModeOIDC:
		oidcConfig.Enabled = true
	case ModeSPIFFE:
		spiffeConfig.Enabled = true
	case ModeDaprSentry:
		daprSentryConfig.Enabled = true
	}

	return Config{
		Mode:       mode,
		OIDC:       oidcConfig,
		SPIFFE:     spiffeConfig,
		DaprSentry: daprSentryConfig,
		envErr:     errors.Join(removedErr, skipPathsErr, oidcErr, spiffeErr, sentryErr),
	}
}

func defaultOIDCConfig() (OIDCConfig, error) {
	algs := slices.Clone(defaultOIDCAlgorithms)
	if raw := os.Getenv(envOIDCAllowedAlgorithms); raw != "" {
		algs = splitList(raw)
	}
	enabled, enabledErr := parseBoolEnv(envOIDCEnabled)
	skipIssuer, skipErr := parseBoolEnv(envOIDCSkipIssuerCheck)

	return OIDCConfig{
		Enabled:           enabled,
		IssuerURL:         os.Getenv(envOIDCIssuerURL),
		ClientID:          os.Getenv(envOIDCClientID),
		AllowedAlgorithms: algs,
		SkipIssuerCheck:   skipIssuer,
	}, errors.Join(enabledErr, skipErr)
}

func defaultSPIFFEConfig() (SPIFFEConfig, error) {
	var allowed []string
	if raw := os.Getenv(envSPIFFEAllowedClients); raw != "" {
		allowed = splitList(raw)
	}
	enabled, err := parseBoolEnv(envSPIFFEEnabled)

	return SPIFFEConfig{
		Enabled:        enabled,
		TrustDomain:    os.Getenv(envSPIFFETrustDomain),
		ServerID:       os.Getenv(envSPIFFEServerID),
		EndpointSocket: os.Getenv(envSPIFFEEndpointSocket),
		AllowedClients: allowed,
	}, err
}

func defaultDaprSentryConfig() (DaprSentryConfig, error) {
	refreshInterval, intervalErr := parseRefreshIntervalEnv()

	tokenHeader := os.Getenv(envSentryTokenHeader)
	if tokenHeader == "" {
		tokenHeader = defaultSentryTokenHeader
	}
	enabled, enabledErr := parseBoolEnv(envSentryEnabled)

	return DaprSentryConfig{
		Enabled:         enabled,
		JWKSUrl:         os.Getenv(envSentryJWKSURL),
		TrustDomain:     os.Getenv(envSentryTrustDomain),
		Audience:        os.Getenv(envSentryAudience),
		Issuer:          os.Getenv(envSentryIssuer),
		TokenHeader:     tokenHeader,
		RefreshInterval: refreshInterval,
	}, errors.Join(enabledErr, intervalErr)
}

func parseRefreshIntervalEnv() (time.Duration, error) {
	raw := os.Getenv(envSentryRefreshInterval)
	if raw == "" {
		return DefaultJWKSRefreshInterval, nil
	}
	parsed, err := time.ParseDuration(raw)
	if err != nil {
		return DefaultJWKSRefreshInterval, fmt.Errorf("%w %q: %w", ErrSentryRefreshInterval, raw, err)
	}
	if parsed <= 0 {
		return DefaultJWKSRefreshInterval, fmt.Errorf("%w %q: must be greater than zero", ErrSentryRefreshInterval, raw)
	}
	return parsed, nil
}

// parseBoolEnv reads a boolean environment variable, treating unset or empty as false.
func parseBoolEnv(name string) (bool, error) {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return false, nil
	}
	v, err := strconv.ParseBool(raw)
	if err != nil {
		return false, fmt.Errorf("parse %s=%q as a boolean: %w", name, raw, err)
	}
	return v, nil
}

// splitList splits a comma separated list, trimming whitespace and dropping empty entries.
func splitList(raw string) []string {
	parts := strings.Split(raw, listSeparator)
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// Validate reports environment parse errors
// and missing settings for the selected authentication mode.
func (c *Config) Validate() error {
	if c.envErr != nil {
		return fmt.Errorf("invalid authentication environment: %w", c.envErr)
	}

	if !c.Enabled() {
		return nil
	}

	switch c.Mode {
	case ModeOIDC:
		if !c.OIDC.Enabled {
			return ErrUnsupportedMethod
		}
		return c.OIDC.Validate()
	case ModeSPIFFE:
		if !c.SPIFFE.Enabled {
			return ErrUnsupportedMethod
		}
		return c.SPIFFE.Validate()
	case ModeDaprSentry:
		if !c.DaprSentry.Enabled {
			return ErrUnsupportedMethod
		}
		return c.DaprSentry.Validate()
	case ModeHybrid:
		if !c.OIDC.Enabled && !c.SPIFFE.Enabled && !c.DaprSentry.Enabled {
			return ErrUnsupportedMethod
		}
		return errors.Join(c.OIDC.Validate(), c.SPIFFE.Validate(), c.DaprSentry.Validate())
	default:
		return fmt.Errorf("%w: unknown AUTH_MODE %q", ErrUnsupportedMethod, c.Mode)
	}
}

// Validate reports missing settings when OIDC is enabled.
func (c *OIDCConfig) Validate() error {
	if !c.Enabled {
		return nil
	}
	if c.IssuerURL == "" {
		return ErrOIDCIssuerURLRequired
	}
	if c.ClientID == "" {
		return ErrOIDCClientIDRequired
	}
	return nil
}

// Validate reports missing settings when SPIFFE is enabled.
func (c *SPIFFEConfig) Validate() error {
	if !c.Enabled {
		return nil
	}
	if c.TrustDomain == "" {
		return ErrSPIFFETrustDomainRequired
	}
	if c.ServerID == "" {
		return ErrSPIFFEServerIDRequired
	}
	return nil
}

// Validate reports missing or invalid settings when Dapr Sentry is enabled.
func (c *DaprSentryConfig) Validate() error {
	if !c.Enabled {
		return nil
	}
	return c.validateSettings()
}

// validateSettings checks the settings regardless of Enabled,
// so the authenticator constructor can enforce them too.
func (c *DaprSentryConfig) validateSettings() error {
	if c.JWKSUrl == "" {
		return ErrSentryJWKSURLRequired
	}
	if c.TrustDomain == "" {
		return ErrSentryTrustDomainRequired
	}
	if c.Audience == "" {
		return ErrSentryAudienceRequired
	}
	if c.RefreshInterval < 0 || (c.RefreshInterval > 0 && c.RefreshInterval < MinJWKSRefreshInterval) {
		return fmt.Errorf("%w %s: must be at least %s", ErrSentryRefreshInterval, c.RefreshInterval, MinJWKSRefreshInterval)
	}
	return nil
}
