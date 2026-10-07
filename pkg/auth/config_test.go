package auth

import (
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func clearAuthEnvVars() {
	envVars := []string{
		"AUTH_ENABLED",
		"AUTH_MODE",
		"AUTH_SKIP_PATHS",
		"OIDC_ENABLED",
		"OIDC_ISSUER_URL",
		"OIDC_CLIENT_ID",
		"OIDC_ALLOWED_ALGORITHMS",
		"OIDC_SKIP_ISSUER_CHECK",
		"SPIFFE_ENABLED",
		"SPIFFE_TRUST_DOMAIN",
		"SPIFFE_SERVER_ID",
		"SPIFFE_ENDPOINT_SOCKET",
		"SPIFFE_ALLOWED_CLIENTS",
		"DAPR_SENTRY_ENABLED",
		"DAPR_SENTRY_JWKS_URL",
		"DAPR_SENTRY_TRUST_DOMAIN",
		"DAPR_SENTRY_AUDIENCE",
		"DAPR_SENTRY_ISSUER",
		"DAPR_SENTRY_TOKEN_HEADER",
		"DAPR_SENTRY_JWKS_REFRESH_INTERVAL",
	}
	for _, v := range envVars {
		os.Unsetenv(v)
	}
}

func TestDefaultConfig(t *testing.T) {
	clearAuthEnvVars()

	cfg := DefaultConfig()

	assert.False(t, cfg.Enabled())
	assert.Equal(t, ModeDisabled, cfg.Mode)
	assert.Contains(t, cfg.SkipPaths, "/livez")
	assert.Contains(t, cfg.SkipPaths, "/readyz")
	assert.Contains(t, cfg.SkipPaths, "/startupz")
}

func TestDefaultConfigWithEnvVars(t *testing.T) {
	clearAuthEnvVars()

	os.Setenv("AUTH_MODE", "oidc")
	os.Setenv("AUTH_SKIP_PATHS", "/health,/metrics")
	defer clearAuthEnvVars()

	cfg := DefaultConfig()

	assert.True(t, cfg.Enabled())
	assert.Equal(t, ModeOIDC, cfg.Mode)
	assert.Contains(t, cfg.SkipPaths, "/health")
	assert.Contains(t, cfg.SkipPaths, "/metrics")
}

func TestDefaultConfigAutoEnablesAuthMethod(t *testing.T) {
	tests := []struct {
		name             string
		authMode         string
		expectOIDC       bool
		expectSPIFFE     bool
		expectDaprSentry bool
	}{
		{
			name:             "oidc mode auto-enables OIDC",
			authMode:         "oidc",
			expectOIDC:       true,
			expectSPIFFE:     false,
			expectDaprSentry: false,
		},
		{
			name:             "spiffe mode auto-enables SPIFFE",
			authMode:         "spiffe",
			expectOIDC:       false,
			expectSPIFFE:     true,
			expectDaprSentry: false,
		},
		{
			name:             "dapr-sentry mode auto-enables DaprSentry",
			authMode:         "dapr-sentry",
			expectOIDC:       false,
			expectSPIFFE:     false,
			expectDaprSentry: true,
		},
		{
			name:             "disabled mode enables nothing",
			authMode:         "disabled",
			expectOIDC:       false,
			expectSPIFFE:     false,
			expectDaprSentry: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clearAuthEnvVars()
			os.Setenv("AUTH_MODE", tt.authMode)
			defer clearAuthEnvVars()

			cfg := DefaultConfig()

			assert.Equal(t, tt.expectOIDC, cfg.OIDC.Enabled, "OIDC.Enabled mismatch")
			assert.Equal(t, tt.expectSPIFFE, cfg.SPIFFE.Enabled, "SPIFFE.Enabled mismatch")
			assert.Equal(t, tt.expectDaprSentry, cfg.DaprSentry.Enabled, "DaprSentry.Enabled mismatch")
		})
	}
}

func TestDefaultConfigHybridModeReadsIndividualFlags(t *testing.T) {
	clearAuthEnvVars()

	// In hybrid mode, individual *_ENABLED flags should be respected
	os.Setenv("AUTH_MODE", "hybrid")
	os.Setenv("OIDC_ENABLED", "true")
	os.Setenv("DAPR_SENTRY_ENABLED", "true")
	// SPIFFE_ENABLED not set, should remain false
	defer clearAuthEnvVars()

	cfg := DefaultConfig()

	assert.True(t, cfg.OIDC.Enabled, "OIDC should be enabled via OIDC_ENABLED")
	assert.False(t, cfg.SPIFFE.Enabled, "SPIFFE should not be enabled")
	assert.True(t, cfg.DaprSentry.Enabled, "DaprSentry should be enabled via DAPR_SENTRY_ENABLED")
}

func TestDefaultOIDCConfig(t *testing.T) {
	clearAuthEnvVars()

	cfg, err := defaultOIDCConfig()
	require.NoError(t, err)

	assert.False(t, cfg.Enabled)
	assert.Empty(t, cfg.IssuerURL)
	assert.Empty(t, cfg.ClientID)
	assert.Contains(t, cfg.AllowedAlgorithms, "RS256")
	assert.Contains(t, cfg.AllowedAlgorithms, "ES256")
	assert.False(t, cfg.SkipIssuerCheck)
}

func TestDefaultOIDCConfigWithEnvVars(t *testing.T) {
	clearAuthEnvVars()

	os.Setenv("OIDC_ENABLED", "true")
	os.Setenv("OIDC_ISSUER_URL", "https://issuer.example.com")
	os.Setenv("OIDC_CLIENT_ID", "my-client")
	os.Setenv("OIDC_ALLOWED_ALGORITHMS", "RS256,RS384,RS512")
	os.Setenv("OIDC_SKIP_ISSUER_CHECK", "true")
	defer clearAuthEnvVars()

	cfg, err := defaultOIDCConfig()
	require.NoError(t, err)

	assert.True(t, cfg.Enabled)
	assert.Equal(t, "https://issuer.example.com", cfg.IssuerURL)
	assert.Equal(t, "my-client", cfg.ClientID)
	assert.Contains(t, cfg.AllowedAlgorithms, "RS256")
	assert.Contains(t, cfg.AllowedAlgorithms, "RS384")
	assert.Contains(t, cfg.AllowedAlgorithms, "RS512")
	assert.True(t, cfg.SkipIssuerCheck)
}

func TestDefaultSPIFFEConfig(t *testing.T) {
	clearAuthEnvVars()

	cfg, err := defaultSPIFFEConfig()
	require.NoError(t, err)

	assert.False(t, cfg.Enabled)
	assert.Empty(t, cfg.TrustDomain)
	assert.Empty(t, cfg.ServerID)
	assert.Empty(t, cfg.EndpointSocket)
	assert.Empty(t, cfg.AllowedClients)
}

func TestDefaultSPIFFEConfigWithEnvVars(t *testing.T) {
	clearAuthEnvVars()

	os.Setenv("SPIFFE_ENABLED", "true")
	os.Setenv("SPIFFE_TRUST_DOMAIN", "cluster.local")
	os.Setenv("SPIFFE_SERVER_ID", "spiffe://cluster.local/server")
	os.Setenv("SPIFFE_ENDPOINT_SOCKET", "unix:///run/spire/sockets/agent.sock")
	os.Setenv("SPIFFE_ALLOWED_CLIENTS", "spiffe://cluster.local/client1,spiffe://cluster.local/client2")
	defer clearAuthEnvVars()

	cfg, err := defaultSPIFFEConfig()
	require.NoError(t, err)

	assert.True(t, cfg.Enabled)
	assert.Equal(t, "cluster.local", cfg.TrustDomain)
	assert.Equal(t, "spiffe://cluster.local/server", cfg.ServerID)
	assert.Equal(t, "unix:///run/spire/sockets/agent.sock", cfg.EndpointSocket)
	assert.Len(t, cfg.AllowedClients, 2)
}

func TestDefaultDaprSentryConfig(t *testing.T) {
	clearAuthEnvVars()

	cfg, err := defaultDaprSentryConfig()
	require.NoError(t, err)

	assert.False(t, cfg.Enabled)
	assert.Empty(t, cfg.JWKSUrl)
	assert.Empty(t, cfg.TrustDomain)
	assert.Empty(t, cfg.Audience)
	assert.Equal(t, "Authorization", cfg.TokenHeader)
	assert.Equal(t, 5*time.Minute, cfg.RefreshInterval)
}

func TestDefaultDaprSentryConfigWithEnvVars(t *testing.T) {
	clearAuthEnvVars()

	os.Setenv("DAPR_SENTRY_ENABLED", "true")
	os.Setenv("DAPR_SENTRY_JWKS_URL", "http://sentry:8080/jwks.json")
	os.Setenv("DAPR_SENTRY_TRUST_DOMAIN", "public")
	os.Setenv("DAPR_SENTRY_AUDIENCE", "public")
	os.Setenv("DAPR_SENTRY_TOKEN_HEADER", "X-Dapr-Token")
	os.Setenv("DAPR_SENTRY_JWKS_REFRESH_INTERVAL", "10m")
	defer clearAuthEnvVars()

	cfg, err := defaultDaprSentryConfig()
	require.NoError(t, err)

	assert.True(t, cfg.Enabled)
	assert.Equal(t, "http://sentry:8080/jwks.json", cfg.JWKSUrl)
	assert.Equal(t, "public", cfg.TrustDomain)
	assert.Equal(t, "public", cfg.Audience)
	assert.Equal(t, "X-Dapr-Token", cfg.TokenHeader)
	assert.Equal(t, 10*time.Minute, cfg.RefreshInterval)
}

func TestConfigValidate(t *testing.T) {
	tests := []struct {
		name    string
		config  Config
		wantErr bool
		errMsg  string
	}{
		{
			name:    "disabled auth is valid",
			config:  Config{Mode: ModeDisabled},
			wantErr: false,
		},
		{
			name:    "zero-value config fails closed",
			config:  Config{},
			wantErr: true,
		},
		{
			name:    "unknown mode fails closed",
			config:  Config{Mode: AuthMode("bogus")},
			wantErr: true,
		},
		{
			name: "oidc mode without oidc enabled",
			config: Config{
				Mode: ModeOIDC,
				OIDC: OIDCConfig{Enabled: false},
			},
			wantErr: true,
		},
		{
			name: "oidc mode with valid config",
			config: Config{
				Mode: ModeOIDC,
				OIDC: OIDCConfig{
					Enabled:   true,
					IssuerURL: "https://issuer.example.com",
					ClientID:  "client-id",
				},
			},
			wantErr: false,
		},
		{
			name: "spiffe mode without spiffe enabled",
			config: Config{
				Mode:   ModeSPIFFE,
				SPIFFE: SPIFFEConfig{Enabled: false},
			},
			wantErr: true,
		},
		{
			name: "spiffe mode with valid config",
			config: Config{
				Mode: ModeSPIFFE,
				SPIFFE: SPIFFEConfig{
					Enabled:     true,
					TrustDomain: "cluster.local",
					ServerID:    "spiffe://cluster.local/server",
				},
			},
			wantErr: false,
		},
		{
			name: "dapr-sentry mode without dapr-sentry enabled",
			config: Config{
				Mode:       ModeDaprSentry,
				DaprSentry: DaprSentryConfig{Enabled: false},
			},
			wantErr: true,
		},
		{
			name: "dapr-sentry mode with valid config",
			config: Config{
				Mode: ModeDaprSentry,
				DaprSentry: DaprSentryConfig{
					Enabled:     true,
					JWKSUrl:     "http://sentry/jwks.json",
					TrustDomain: "public",
					Audience:    "public",
				},
			},
			wantErr: false,
		},
		{
			name: "hybrid mode with no methods enabled",
			config: Config{
				Mode:       ModeHybrid,
				OIDC:       OIDCConfig{Enabled: false},
				SPIFFE:     SPIFFEConfig{Enabled: false},
				DaprSentry: DaprSentryConfig{Enabled: false},
			},
			wantErr: true,
		},
		{
			name: "hybrid mode with oidc enabled",
			config: Config{
				Mode: ModeHybrid,
				OIDC: OIDCConfig{
					Enabled:   true,
					IssuerURL: "https://issuer.example.com",
					ClientID:  "client-id",
				},
			},
			wantErr: false,
		},
		{
			name: "hybrid mode with invalid oidc config",
			config: Config{
				Mode: ModeHybrid,
				OIDC: OIDCConfig{
					Enabled:   true,
					IssuerURL: "", // Missing required field
					ClientID:  "client-id",
				},
			},
			wantErr: true,
		},
		{
			name: "unknown mode",
			config: Config{
				Mode: AuthMode("unknown"),
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.config.Validate()

			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestOIDCConfigValidate(t *testing.T) {
	tests := []struct {
		name    string
		config  OIDCConfig
		wantErr bool
		errMsg  string
	}{
		{
			name:    "disabled is valid",
			config:  OIDCConfig{Enabled: false},
			wantErr: false,
		},
		{
			name: "missing issuer URL",
			config: OIDCConfig{
				Enabled:   true,
				IssuerURL: "",
				ClientID:  "client-id",
			},
			wantErr: true,
			errMsg:  "OIDC_ISSUER_URL is required",
		},
		{
			name: "missing client ID",
			config: OIDCConfig{
				Enabled:   true,
				IssuerURL: "https://issuer.example.com",
				ClientID:  "",
			},
			wantErr: true,
			errMsg:  "OIDC_CLIENT_ID is required",
		},
		{
			name: "valid config",
			config: OIDCConfig{
				Enabled:   true,
				IssuerURL: "https://issuer.example.com",
				ClientID:  "client-id",
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.config.Validate()

			if tt.wantErr {
				assert.Error(t, err)
				if tt.errMsg != "" {
					assert.Contains(t, err.Error(), tt.errMsg)
				}
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestSPIFFEConfigValidate(t *testing.T) {
	tests := []struct {
		name    string
		config  SPIFFEConfig
		wantErr bool
		errMsg  string
	}{
		{
			name:    "disabled is valid",
			config:  SPIFFEConfig{Enabled: false},
			wantErr: false,
		},
		{
			name: "missing trust domain",
			config: SPIFFEConfig{
				Enabled:     true,
				TrustDomain: "",
				ServerID:    "spiffe://cluster.local/server",
			},
			wantErr: true,
			errMsg:  "SPIFFE_TRUST_DOMAIN is required",
		},
		{
			name: "missing server ID",
			config: SPIFFEConfig{
				Enabled:     true,
				TrustDomain: "cluster.local",
				ServerID:    "",
			},
			wantErr: true,
			errMsg:  "SPIFFE_SERVER_ID is required",
		},
		{
			name: "valid config",
			config: SPIFFEConfig{
				Enabled:     true,
				TrustDomain: "cluster.local",
				ServerID:    "spiffe://cluster.local/server",
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.config.Validate()

			if tt.wantErr {
				assert.Error(t, err)
				if tt.errMsg != "" {
					assert.Contains(t, err.Error(), tt.errMsg)
				}
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestDaprSentryConfigValidate(t *testing.T) {
	tests := []struct {
		name    string
		config  DaprSentryConfig
		wantErr bool
		errMsg  string
	}{
		{
			name:    "disabled is valid",
			config:  DaprSentryConfig{Enabled: false},
			wantErr: false,
		},
		{
			name: "missing JWKS URL",
			config: DaprSentryConfig{
				Enabled:     true,
				JWKSUrl:     "",
				TrustDomain: "public",
			},
			wantErr: true,
			errMsg:  "DAPR_SENTRY_JWKS_URL is required",
		},
		{
			name: "missing trust domain",
			config: DaprSentryConfig{
				Enabled:     true,
				JWKSUrl:     "http://sentry/jwks.json",
				TrustDomain: "",
			},
			wantErr: true,
			errMsg:  "DAPR_SENTRY_TRUST_DOMAIN is required",
		},
		{
			name: "valid config",
			config: DaprSentryConfig{
				Enabled:     true,
				JWKSUrl:     "http://sentry/jwks.json",
				TrustDomain: "public",
				Audience:    "public",
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.config.Validate()

			if tt.wantErr {
				assert.Error(t, err)
				if tt.errMsg != "" {
					assert.Contains(t, err.Error(), tt.errMsg)
				}
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestDefaultConfigSkipPathsSpaceTrimming(t *testing.T) {
	clearAuthEnvVars()

	os.Setenv("AUTH_SKIP_PATHS", " /path1 , /path2 , /path3 ")
	defer clearAuthEnvVars()

	cfg := DefaultConfig()

	assert.Contains(t, cfg.SkipPaths, "/path1")
	assert.Contains(t, cfg.SkipPaths, "/path2")
	assert.Contains(t, cfg.SkipPaths, "/path3")
}

func TestHybridModeWithMultipleMethodsEnabled(t *testing.T) {
	config := Config{
		Mode: ModeHybrid,
		OIDC: OIDCConfig{
			Enabled:   true,
			IssuerURL: "https://issuer.example.com",
			ClientID:  "client-id",
		},
		SPIFFE: SPIFFEConfig{
			Enabled:     true,
			TrustDomain: "cluster.local",
			ServerID:    "spiffe://cluster.local/server",
		},
		DaprSentry: DaprSentryConfig{
			Enabled:     true,
			JWKSUrl:     "http://sentry/jwks.json",
			TrustDomain: "public",
			Audience:    "public",
		},
	}

	err := config.Validate()
	assert.NoError(t, err)
}
