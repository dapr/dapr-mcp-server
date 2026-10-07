package auth

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDefaultConfigEnvValidation(t *testing.T) {
	tests := []struct {
		name        string
		env         map[string]string
		wantErr     error
		errMsg      string
		wantEnabled bool
	}{
		{
			name: "no mode is disabled and valid",
			env:  map[string]string{},
		},
		{
			name: "mode disabled is valid",
			env:  map[string]string{envAuthMode: "disabled"},
		},
		{
			name:        "oidc mode enables",
			env:         map[string]string{envAuthMode: "oidc", envOIDCIssuerURL: "https://issuer", envOIDCClientID: "c"},
			wantEnabled: true,
		},
		{
			name:        "spiffe mode enables",
			env:         map[string]string{envAuthMode: "spiffe", envSPIFFETrustDomain: "example.test", envSPIFFEServerID: "spiffe://example.test/server"},
			wantEnabled: true,
		},
		{
			name: "dapr-sentry mode enables",
			env: map[string]string{
				envAuthMode: "dapr-sentry", envSentryJWKSURL: "http://sentry",
				envSentryTrustDomain: "public", envSentryAudience: "public",
			},
			wantEnabled: true,
		},
		{
			name:        "hybrid mode with one method enabled",
			env:         map[string]string{envAuthMode: "hybrid", envOIDCEnabled: "true", envOIDCIssuerURL: "https://issuer", envOIDCClientID: "c"},
			wantEnabled: true,
		},
		{
			name:        "hybrid mode with no method enabled",
			env:         map[string]string{envAuthMode: "hybrid"},
			wantErr:     ErrUnsupportedMethod,
			wantEnabled: true,
		},
		{
			name:        "typo in mode fails closed",
			env:         map[string]string{envAuthMode: "oidcc"},
			wantErr:     ErrUnsupportedMethod,
			errMsg:      "oidcc",
			wantEnabled: true,
		},
		{
			name:        "mode is case and space insensitive",
			env:         map[string]string{envAuthMode: " OIDC ", envOIDCIssuerURL: "https://issuer", envOIDCClientID: "c"},
			wantEnabled: true,
		},
		{
			name:    "removed AUTH_ENABLED true",
			env:     map[string]string{envAuthEnabled: "true"},
			wantErr: ErrAuthEnabledRemoved,
		},
		{
			name:    "removed AUTH_ENABLED false",
			env:     map[string]string{envAuthEnabled: "false"},
			wantErr: ErrAuthEnabledRemoved,
		},
		{
			name:        "removed AUTH_ENABLED alongside a mode",
			env:         map[string]string{envAuthEnabled: "true", envAuthMode: "oidc", envOIDCIssuerURL: "https://issuer", envOIDCClientID: "c"},
			wantErr:     ErrAuthEnabledRemoved,
			wantEnabled: true,
		},
		{
			name:   "invalid boolean for sentry enabled",
			env:    map[string]string{envSentryEnabled: "on"},
			errMsg: envSentryEnabled,
		},
		{
			name:    "unparseable refresh interval",
			env:     map[string]string{envSentryRefreshInterval: "soon"},
			wantErr: ErrSentryRefreshInterval,
		},
		{
			name:    "zero refresh interval",
			env:     map[string]string{envSentryRefreshInterval: "0s"},
			wantErr: ErrSentryRefreshInterval,
		},
		{
			name:    "negative refresh interval",
			env:     map[string]string{envSentryRefreshInterval: "-1m"},
			wantErr: ErrSentryRefreshInterval,
		},
		{
			name: "refresh interval below floor",
			env: map[string]string{
				envAuthMode:      "dapr-sentry",
				envSentryJWKSURL: "http://sentry", envSentryTrustDomain: "public",
				envSentryAudience: "public", envSentryRefreshInterval: "1s",
			},
			wantErr:     ErrSentryRefreshInterval,
			wantEnabled: true,
		},
		{
			name: "sentry without audience",
			env: map[string]string{
				envAuthMode:      "dapr-sentry",
				envSentryJWKSURL: "http://sentry", envSentryTrustDomain: "public",
			},
			wantErr:     ErrSentryAudienceRequired,
			wantEnabled: true,
		},
		{
			name:    "skip paths catch-all",
			env:     map[string]string{envAuthSkipPaths: "/livez,*"},
			wantErr: ErrInvalidSkipPath,
		},
		{
			name:    "skip paths root wildcard",
			env:     map[string]string{envAuthSkipPaths: "/*"},
			wantErr: ErrInvalidSkipPath,
		},
		{
			name:    "skip path without leading slash",
			env:     map[string]string{envAuthSkipPaths: "livez"},
			wantErr: ErrInvalidSkipPath,
		},
		{
			name: "skip paths with trailing commas",
			env:  map[string]string{envAuthSkipPaths: "/livez,, ,/readyz,"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clearAuthEnvVars()
			for k, v := range tt.env {
				t.Setenv(k, v)
			}

			cfg := DefaultConfig()
			err := cfg.Validate()
			assert.Equal(t, tt.wantEnabled, cfg.Enabled())

			switch {
			case tt.wantErr != nil:
				require.ErrorIs(t, err, tt.wantErr)
				if tt.errMsg != "" {
					require.ErrorContains(t, err, tt.errMsg)
				}
			case tt.errMsg != "":
				require.ErrorContains(t, err, tt.errMsg)
			default:
				require.NoError(t, err)
			}
		})
	}
}

func TestDefaultConfigSkipPathsDropsEmptyEntries(t *testing.T) {
	clearAuthEnvVars()
	t.Setenv(envAuthSkipPaths, " /livez ,, ,/readyz,")

	cfg := DefaultConfig()

	assert.Equal(t, []string{"/livez", "/readyz"}, cfg.SkipPaths)
}

func TestDefaultConfigSkipPathsIsACopy(t *testing.T) {
	clearAuthEnvVars()

	cfg := DefaultConfig()
	cfg.SkipPaths[0] = "/mutated"

	assert.Equal(t, "/livez", DefaultConfig().SkipPaths[0])
}

func TestDefaultDaprSentryConfigReadsIssuer(t *testing.T) {
	clearAuthEnvVars()
	t.Setenv(envSentryIssuer, "https://sentry.example")

	cfg, err := defaultDaprSentryConfig()

	require.NoError(t, err)
	assert.Equal(t, "https://sentry.example", cfg.Issuer)
	assert.Equal(t, DefaultJWKSRefreshInterval, cfg.RefreshInterval)
}

func TestDaprSentryConfigValidateRefreshInterval(t *testing.T) {
	t.Parallel()

	base := DaprSentryConfig{Enabled: true, JWKSUrl: "http://sentry", TrustDomain: "public", Audience: "public"}
	tests := []struct {
		name     string
		interval time.Duration
		wantErr  bool
	}{
		{name: "zero uses default", interval: 0},
		{name: "at floor", interval: MinJWKSRefreshInterval},
		{name: "below floor", interval: time.Second, wantErr: true},
		{name: "negative", interval: -time.Minute, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cfg := base
			cfg.RefreshInterval = tt.interval
			err := cfg.Validate()
			if tt.wantErr {
				require.ErrorIs(t, err, ErrSentryRefreshInterval)
				return
			}
			require.NoError(t, err)
		})
	}
}
