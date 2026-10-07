//go:build integration

package integration

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/dapr/dapr-mcp-server/pkg/auth"
	"github.com/dapr/dapr-mcp-server/test/mocks"
)

// TestDaprSentryIntegration tests the full authentication flow
func TestDaprSentryIntegration(t *testing.T) {
	// Generate RSA key pair
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)

	// Create JWKS
	jwk := jose.JSONWebKey{
		Key:       &privateKey.PublicKey,
		KeyID:     "sentry-key-1",
		Algorithm: string(jose.RS256),
		Use:       "sig",
	}
	jwks := jose.JSONWebKeySet{Keys: []jose.JSONWebKey{jwk}}

	// Start mock JWKS server
	jwksServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Logf("JWKS request: %s %s", r.Method, r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(jwks)
	}))
	defer jwksServer.Close()

	// Configure authenticator
	cfg := auth.DaprSentryConfig{
		Enabled:         true,
		JWKSUrl:         jwksServer.URL + "/jwks.json",
		TrustDomain:     "public",
		Audience:        "public",
		TokenHeader:     "X-My-Auth",
		RefreshInterval: 5 * time.Minute,
	}

	authenticator, err := auth.NewDaprSentryAuthenticator(context.Background(), cfg)
	require.NoError(t, err)

	// Create a valid JWT (simulating Dapr Sentry token)
	token := createSentryJWT(t, privateKey, "sentry-key-1", jwt.Claims{
		Subject:   "spiffe://public/ns/default/17c9f2b4-859d-4249-8701-7e846540e704",
		Audience:  jwt.Audience{"public"},
		Expiry:    jwt.NewNumericDate(time.Now().Add(1 * time.Hour)),
		IssuedAt:  jwt.NewNumericDate(time.Now()),
		NotBefore: jwt.NewNumericDate(time.Now().Add(-1 * time.Minute)),
		ID:        "test-jwt-id",
	})

	// Test authentication
	identity, err := authenticator.Authenticate(context.Background(), token)
	require.NoError(t, err)

	t.Logf("Authenticated identity: %+v", identity)
	require.Equal(t, "spiffe://public/ns/default/17c9f2b4-859d-4249-8701-7e846540e704", identity.Subject)
	require.Equal(t, auth.ModeDaprSentry, identity.AuthMethod)
}

// TestMiddlewareWithCustomHeader tests the middleware extracts tokens from custom headers
func TestMiddlewareWithCustomHeader(t *testing.T) {
	// Generate RSA key pair
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)

	// Create JWKS
	jwk := jose.JSONWebKey{
		Key:       &privateKey.PublicKey,
		KeyID:     "sentry-key-1",
		Algorithm: string(jose.RS256),
		Use:       "sig",
	}
	jwks := jose.JSONWebKeySet{Keys: []jose.JSONWebKey{jwk}}

	// Start mock JWKS server
	jwksServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(jwks)
	}))
	defer jwksServer.Close()

	// Configure auth
	cfg := auth.Config{
		Mode: auth.ModeDaprSentry,
		DaprSentry: auth.DaprSentryConfig{
			Enabled:         true,
			JWKSUrl:         jwksServer.URL + "/jwks.json",
			TrustDomain:     "public",
			Audience:        "public",
			TokenHeader:     "X-My-Auth",
			RefreshInterval: 5 * time.Minute,
		},
	}

	require.NoError(t, cfg.Validate())
	authenticator, err := auth.NewDaprSentryAuthenticator(context.Background(), cfg.DaprSentry)
	require.NoError(t, err)
	t.Cleanup(func() { _ = auth.CloseAuthenticators([]auth.Authenticator{authenticator}) })

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelDebug}))
	middleware := auth.NewMiddleware(cfg, []auth.Authenticator{authenticator}, logger)

	// Create test handler
	handler := middleware.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		identity := auth.GetIdentity(r.Context())
		if identity != nil {
			w.WriteHeader(http.StatusOK)
			fmt.Fprintf(w, "Authenticated: %s", identity.Subject)
		} else {
			w.WriteHeader(http.StatusUnauthorized)
		}
	}))

	// Create valid token
	token := createSentryJWT(t, privateKey, "sentry-key-1", jwt.Claims{
		Subject:  "spiffe://public/ns/default/test-uuid",
		Audience: jwt.Audience{"public"},
		Expiry:   jwt.NewNumericDate(time.Now().Add(1 * time.Hour)),
	})

	// Test with custom header
	req := httptest.NewRequest("GET", "/api/test", nil)
	req.Header.Set("X-My-Auth", token)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), "spiffe://public/ns/default/test-uuid")

	// A validly signed token without exp must be rejected.
	noExpiry := createSentryJWT(t, privateKey, "sentry-key-1", jwt.Claims{
		Subject:  "spiffe://public/ns/default/test-uuid",
		Audience: jwt.Audience{"public"},
	})
	req = httptest.NewRequest("GET", "/api/test", nil)
	req.Header.Set("X-My-Auth", noExpiry)
	rec = httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	require.Equal(t, http.StatusUnauthorized, rec.Code)
}

// TestMiddlewareWithMockAuthenticator checks the shared mock works without a Mode expectation.
func TestMiddlewareWithMockAuthenticator(t *testing.T) {
	mockAuth := new(mocks.MockAuthenticator)
	mockAuth.On("Authenticate", mock.Anything, "bad-token").Return(nil, auth.ErrInvalidToken)

	middleware := auth.NewMiddleware(auth.Config{Mode: auth.ModeOIDC}, []auth.Authenticator{mockAuth}, nil)
	handler := middleware.Handler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/mcp", nil)
	req.Header.Set("Authorization", "Bearer bad-token")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	require.Equal(t, http.StatusUnauthorized, rec.Code)
	require.Equal(t, auth.AuthMode(""), mockAuth.Mode())
	mockAuth.AssertExpectations(t)
}

func createSentryJWT(t *testing.T, privateKey *rsa.PrivateKey, keyID string, claims jwt.Claims) string {
	t.Helper()

	signerOpts := jose.SignerOptions{}
	signerOpts.WithType("JWT")
	signerOpts.WithHeader("kid", keyID)

	signer, err := jose.NewSigner(jose.SigningKey{
		Algorithm: jose.RS256,
		Key:       privateKey,
	}, &signerOpts)
	require.NoError(t, err)

	token, err := jwt.Signed(signer).Claims(claims).Serialize()
	require.NoError(t, err)

	return token
}
