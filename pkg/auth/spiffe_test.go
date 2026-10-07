package auth

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestSPIFFEAuthenticator_Mode(t *testing.T) {
	auth := &SPIFFEAuthenticator{
		config: SPIFFEConfig{
			TrustDomain: "cluster.local",
		},
	}

	mode := auth.Mode()

	assert.Equal(t, ModeSPIFFE, mode)
}

func TestSPIFFEConfigStruct(t *testing.T) {
	cfg := SPIFFEConfig{
		Enabled:        true,
		TrustDomain:    "cluster.local",
		ServerID:       "spiffe://cluster.local/ns/default/sa/server",
		EndpointSocket: "unix:///run/spire/sockets/agent.sock",
		AllowedClients: []string{
			"spiffe://cluster.local/ns/default/sa/client1",
			"spiffe://cluster.local/ns/default/sa/client2",
		},
	}

	assert.True(t, cfg.Enabled)
	assert.Equal(t, "cluster.local", cfg.TrustDomain)
	assert.Equal(t, "spiffe://cluster.local/ns/default/sa/server", cfg.ServerID)
	assert.Equal(t, "unix:///run/spire/sockets/agent.sock", cfg.EndpointSocket)
	assert.Len(t, cfg.AllowedClients, 2)
}

func TestSPIFFEConnectionTimeout(t *testing.T) {
	assert.Equal(t, 10*time.Second, SPIFFEConnectionTimeout)
}

func TestSPIFFEConfigValidation(t *testing.T) {
	tests := []struct {
		name    string
		config  SPIFFEConfig
		wantErr bool
		errMsg  string
	}{
		{
			name: "valid config",
			config: SPIFFEConfig{
				Enabled:     true,
				TrustDomain: "cluster.local",
				ServerID:    "spiffe://cluster.local/server",
			},
			wantErr: false,
		},
		{
			name: "disabled config",
			config: SPIFFEConfig{
				Enabled: false,
			},
			wantErr: false,
		},
		{
			name: "missing trust domain",
			config: SPIFFEConfig{
				Enabled:  true,
				ServerID: "spiffe://cluster.local/server",
			},
			wantErr: true,
			errMsg:  "SPIFFE_TRUST_DOMAIN is required",
		},
		{
			name: "missing server ID",
			config: SPIFFEConfig{
				Enabled:     true,
				TrustDomain: "cluster.local",
			},
			wantErr: true,
			errMsg:  "SPIFFE_SERVER_ID is required",
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

func TestSPIFFEConfigDefaults(t *testing.T) {
	cfg := SPIFFEConfig{}

	assert.False(t, cfg.Enabled)
	assert.Empty(t, cfg.TrustDomain)
	assert.Empty(t, cfg.ServerID)
	assert.Empty(t, cfg.EndpointSocket)
	assert.Nil(t, cfg.AllowedClients)
}

func TestSPIFFEAuthenticatorModeReturnsCorrectValue(t *testing.T) {
	auth := &SPIFFEAuthenticator{}

	// Mode should always return ModeSPIFFE regardless of config
	assert.Equal(t, ModeSPIFFE, auth.Mode())
}

func TestSPIFFEConfigWithEndpointSocket(t *testing.T) {
	cfg := SPIFFEConfig{
		Enabled:        true,
		TrustDomain:    "example.org",
		ServerID:       "spiffe://example.org/server",
		EndpointSocket: "unix:///custom/path/agent.sock",
	}

	err := cfg.Validate()
	assert.NoError(t, err)
	assert.Equal(t, "unix:///custom/path/agent.sock", cfg.EndpointSocket)
}

func TestSPIFFEConfigWithAllowedClients(t *testing.T) {
	cfg := SPIFFEConfig{
		Enabled:     true,
		TrustDomain: "example.org",
		ServerID:    "spiffe://example.org/server",
		AllowedClients: []string{
			"spiffe://example.org/client1",
			"spiffe://example.org/client2",
			"spiffe://example.org/client3",
		},
	}

	err := cfg.Validate()
	assert.NoError(t, err)
	assert.Len(t, cfg.AllowedClients, 3)
}
