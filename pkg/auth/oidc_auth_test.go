package auth

import (
	"bytes"
	"context"
	"crypto/rsa"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	testOIDCClientID = "mcp-client"
	oidcKeyID        = "oidc-key"
)

type oidcClaims struct {
	jwt.Claims
	Email         string `json:"email,omitempty"`
	EmailVerified bool   `json:"email_verified"`
	Name          string `json:"name,omitempty"`
}

// newOIDCProvider serves OIDC discovery and a JWKS for a freshly generated RSA key.
func newOIDCProvider(t *testing.T) (*httptest.Server, *rsa.PrivateKey) {
	t.Helper()
	privateKey, jwk := generateTestKey(t)
	jwk.KeyID = oidcKeyID

	mux := http.NewServeMux()
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer":                                server.URL,
			"jwks_uri":                              server.URL + "/keys",
			"authorization_endpoint":                server.URL + "/auth",
			"token_endpoint":                        server.URL + "/token",
			"id_token_signing_alg_values_supported": []string{"RS256"},
		})
	})
	mux.HandleFunc("/keys", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{jwk}})
	})
	return server, privateKey
}

func validOIDCClaims(issuer string) oidcClaims {
	now := time.Now()
	return oidcClaims{
		Claims: jwt.Claims{
			Issuer:   issuer,
			Subject:  "user-123",
			Audience: jwt.Audience{testOIDCClientID},
			Expiry:   jwt.NewNumericDate(now.Add(time.Hour)),
			IssuedAt: jwt.NewNumericDate(now),
		},
		Email:         "user@example.com",
		EmailVerified: true,
		Name:          "Test User",
	}
}

func TestOIDCAuthenticator_Authenticate(t *testing.T) {
	t.Parallel()
	server, privateKey := newOIDCProvider(t)
	a, err := NewOIDCAuthenticator(context.Background(), OIDCConfig{
		Enabled:           true,
		IssuerURL:         server.URL,
		ClientID:          testOIDCClientID,
		AllowedAlgorithms: []string{"RS256"},
	})
	require.NoError(t, err)

	tests := []struct {
		name      string
		mutate    func(*oidcClaims)
		wantErr   bool
		wantEmail string
	}{
		{name: "valid", mutate: func(*oidcClaims) {}, wantEmail: "user@example.com"},
		{name: "unverified email dropped", mutate: func(c *oidcClaims) { c.EmailVerified = false }},
		{name: "issuer mismatch", mutate: func(c *oidcClaims) { c.Issuer = "https://evil.example" }, wantErr: true},
		{name: "audience mismatch", mutate: func(c *oidcClaims) { c.Audience = jwt.Audience{"other"} }, wantErr: true},
		{name: "expired", mutate: func(c *oidcClaims) { c.Expiry = jwt.NewNumericDate(time.Now().Add(-time.Hour)) }, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			claims := validOIDCClaims(server.URL)
			tt.mutate(&claims)
			token := signToken(t, jose.RS256, privateKey, oidcKeyID, claims)

			identity, err := a.Authenticate(context.Background(), token)

			if tt.wantErr {
				require.ErrorIs(t, err, ErrInvalidToken)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, "user-123", identity.Subject)
			assert.Equal(t, server.URL, identity.Issuer)
			assert.Equal(t, "Test User", identity.Name)
			assert.Equal(t, tt.wantEmail, identity.Email)
			assert.Equal(t, ModeOIDC, identity.AuthMethod)
			assert.Equal(t, "user-123", identity.Claims["sub"])
		})
	}
}

func TestOIDCAuthenticator_RejectsForeignSignature(t *testing.T) {
	t.Parallel()
	server, _ := newOIDCProvider(t)
	otherKey, _ := generateTestKey(t)
	a, err := NewOIDCAuthenticator(context.Background(), OIDCConfig{IssuerURL: server.URL, ClientID: testOIDCClientID})
	require.NoError(t, err)

	token := signToken(t, jose.RS256, otherKey, oidcKeyID, validOIDCClaims(server.URL))

	_, err = a.Authenticate(context.Background(), token)
	require.ErrorIs(t, err, ErrInvalidToken)
}

func TestOIDCAuthenticator_SkipIssuerCheckWarns(t *testing.T) {
	t.Parallel()
	server, _ := newOIDCProvider(t)
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))

	_, err := NewOIDCAuthenticatorWithLogger(context.Background(), OIDCConfig{
		IssuerURL:       server.URL,
		ClientID:        testOIDCClientID,
		SkipIssuerCheck: true,
	}, logger)

	require.NoError(t, err)
	assert.Contains(t, buf.String(), "level=WARN")
	assert.Contains(t, buf.String(), "issuer check is disabled")
}

func TestNewOIDCAuthenticator_DiscoveryFailure(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(server.Close)

	_, err := NewOIDCAuthenticator(context.Background(), OIDCConfig{IssuerURL: server.URL, ClientID: testOIDCClientID})

	require.ErrorContains(t, err, "create OIDC provider")
}
