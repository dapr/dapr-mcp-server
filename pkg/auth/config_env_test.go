package auth

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDefaultConfigEnvValidation(t *testing.T) {
	tests := []struct {
		name    string
		env     map[string]string
		wantErr error
		errMsg  string
	}{
		{
			name:    "mode set without enabled",
			env:     map[string]string{envAuthMode: "oidc", envOIDCIssuerURL: "https://issuer", envOIDCClientID: "c"},
			wantErr: ErrModeWithoutEnabled,
		},
		{
			name:    "enabled without mode",
			env:     map[string]string{envAuthEnabled: "true"},
			wantErr: ErrEnabledWithoutMode,
		},
		{
			name:    "enabled with mode disabled",
			env:     map[string]string{envAuthEnabled: "true", envAuthMode: "disabled"},
			wantErr: ErrEnabledWithoutMode,
		},
		{
			name: "mode disabled without enabled",
			env:  map[string]string{envAuthMode: "disabled"},
		},
		{
			name:   "invalid boolean for enabled",
			env:    map[string]string{envAuthEnabled: "yes please", envAuthMode: "oidc"},
			errMsg: envAuthEnabled,
		},
		{
			name: "enabled accepts 1",
			env: map[string]string{
				envAuthEnabled: "1", envAuthMode: "oidc",
				envOIDCIssuerURL: "https://issuer", envOIDCClientID: "c",
			},
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
				envAuthEnabled: "true", envAuthMode: "dapr-sentry",
				envSentryJWKSURL: "http://sentry", envSentryTrustDomain: "public",
				envSentryAudience: "public", envSentryRefreshInterval: "1s",
			},
			wantErr: ErrSentryRefreshInterval,
		},
		{
			name: "sentry without audience",
			env: map[string]string{
				envAuthEnabled: "true", envAuthMode: "dapr-sentry",
				envSentryJWKSURL: "http://sentry", envSentryTrustDomain: "public",
			},
			wantErr: ErrSentryAudienceRequired,
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

			switch {
			case tt.wantErr != nil:
				require.ErrorIs(t, err, tt.wantErr)
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
