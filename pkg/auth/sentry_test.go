package auth

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func generateTestKey(t *testing.T) (*rsa.PrivateKey, jose.JSONWebKey) {
	t.Helper()

	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)

	jwk := jose.JSONWebKey{
		Key:       &privateKey.PublicKey,
		KeyID:     "test-key-id",
		Algorithm: string(jose.RS256),
		Use:       "sig",
	}

	return privateKey, jwk
}

func createTestJWT(t *testing.T, privateKey *rsa.PrivateKey, keyID string, claims interface{}) string {
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

func setupJWKSServer(t *testing.T, jwks jose.JSONWebKeySet) *httptest.Server {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		err := json.NewEncoder(w).Encode(jwks)
		require.NoError(t, err)
	}))

	t.Cleanup(server.Close)
	return server
}

func TestDaprSentryAuthenticator_Authenticate_Success(t *testing.T) {
	privateKey, jwk := generateTestKey(t)

	jwks := jose.JSONWebKeySet{Keys: []jose.JSONWebKey{jwk}}
	server := setupJWKSServer(t, jwks)

	cfg := DaprSentryConfig{
		Enabled:         true,
		JWKSUrl:         server.URL,
		TrustDomain:     "public",
		Audience:        "public",
		RefreshInterval: 5 * time.Minute,
	}

	authenticator, err := NewDaprSentryAuthenticator(context.Background(), cfg)
	require.NoError(t, err)

	claims := daprSentryClaims{
		Claims: jwt.Claims{
			Subject:   "spiffe://public/ns/default/17c9f2b4-859d-4249-8701-7e846540e704",
			Audience:  jwt.Audience{"public"},
			Expiry:    jwt.NewNumericDate(time.Now().Add(1 * time.Hour)),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
			NotBefore: jwt.NewNumericDate(time.Now().Add(-1 * time.Minute)),
			ID:        "4a4c85636a3328bf0db29a40a6fda63a",
		},
		Use: "sig",
	}

	token := createTestJWT(t, privateKey, "test-key-id", claims)

	identity, err := authenticator.Authenticate(context.Background(), token)
	require.NoError(t, err)
	assert.Equal(t, "spiffe://public/ns/default/17c9f2b4-859d-4249-8701-7e846540e704", identity.Subject)
	assert.Equal(t, ModeDaprSentry, identity.AuthMethod)
	assert.Contains(t, identity.Audience, "public")
	assert.Equal(t, "sig", identity.Claims["use"])
	assert.Equal(t, "4a4c85636a3328bf0db29a40a6fda63a", identity.Claims["jti"])
}

func TestDaprSentryAuthenticator_Authenticate_ExpiredToken(t *testing.T) {
	privateKey, jwk := generateTestKey(t)

	jwks := jose.JSONWebKeySet{Keys: []jose.JSONWebKey{jwk}}
	server := setupJWKSServer(t, jwks)

	cfg := DaprSentryConfig{
		Enabled:         true,
		JWKSUrl:         server.URL,
		TrustDomain:     "public",
		RefreshInterval: 5 * time.Minute,
	}

	authenticator, err := NewDaprSentryAuthenticator(context.Background(), cfg)
	require.NoError(t, err)

	claims := daprSentryClaims{
		Claims: jwt.Claims{
			Subject:  "spiffe://public/ns/default/test-uuid",
			Audience: jwt.Audience{"public"},
			Expiry:   jwt.NewNumericDate(time.Now().Add(-1 * time.Hour)), // Expired
			IssuedAt: jwt.NewNumericDate(time.Now().Add(-2 * time.Hour)),
		},
	}

	token := createTestJWT(t, privateKey, "test-key-id", claims)

	_, err = authenticator.Authenticate(context.Background(), token)
	assert.ErrorIs(t, err, ErrTokenExpired)
}

func TestDaprSentryAuthenticator_Authenticate_NotYetValid(t *testing.T) {
	privateKey, jwk := generateTestKey(t)

	jwks := jose.JSONWebKeySet{Keys: []jose.JSONWebKey{jwk}}
	server := setupJWKSServer(t, jwks)

	cfg := DaprSentryConfig{
		Enabled:         true,
		JWKSUrl:         server.URL,
		TrustDomain:     "public",
		RefreshInterval: 5 * time.Minute,
	}

	authenticator, err := NewDaprSentryAuthenticator(context.Background(), cfg)
	require.NoError(t, err)

	claims := daprSentryClaims{
		Claims: jwt.Claims{
			Subject:   "spiffe://public/ns/default/test-uuid",
			Audience:  jwt.Audience{"public"},
			Expiry:    jwt.NewNumericDate(time.Now().Add(2 * time.Hour)),
			NotBefore: jwt.NewNumericDate(time.Now().Add(1 * time.Hour)), // Not yet valid
		},
	}

	token := createTestJWT(t, privateKey, "test-key-id", claims)

	_, err = authenticator.Authenticate(context.Background(), token)
	assert.ErrorIs(t, err, ErrInvalidToken)
	assert.Contains(t, err.Error(), "not yet valid")
}

func TestDaprSentryAuthenticator_Authenticate_InvalidAudience(t *testing.T) {
	privateKey, jwk := generateTestKey(t)

	jwks := jose.JSONWebKeySet{Keys: []jose.JSONWebKey{jwk}}
	server := setupJWKSServer(t, jwks)

	cfg := DaprSentryConfig{
		Enabled:         true,
		JWKSUrl:         server.URL,
		TrustDomain:     "public",
		Audience:        "expected-audience",
		RefreshInterval: 5 * time.Minute,
	}

	authenticator, err := NewDaprSentryAuthenticator(context.Background(), cfg)
	require.NoError(t, err)

	claims := daprSentryClaims{
		Claims: jwt.Claims{
			Subject:  "spiffe://public/ns/default/test-uuid",
			Audience: jwt.Audience{"wrong-audience"},
			Expiry:   jwt.NewNumericDate(time.Now().Add(1 * time.Hour)),
		},
	}

	token := createTestJWT(t, privateKey, "test-key-id", claims)

	_, err = authenticator.Authenticate(context.Background(), token)
	assert.ErrorIs(t, err, ErrInvalidAudience)
}

func TestDaprSentryAuthenticator_Authenticate_WrongTrustDomain(t *testing.T) {
	privateKey, jwk := generateTestKey(t)

	jwks := jose.JSONWebKeySet{Keys: []jose.JSONWebKey{jwk}}
	server := setupJWKSServer(t, jwks)

	cfg := DaprSentryConfig{
		Enabled:         true,
		JWKSUrl:         server.URL,
		TrustDomain:     "expected-domain",
		RefreshInterval: 5 * time.Minute,
	}

	authenticator, err := NewDaprSentryAuthenticator(context.Background(), cfg)
	require.NoError(t, err)

	claims := daprSentryClaims{
		Claims: jwt.Claims{
			Subject:  "spiffe://wrong-domain/ns/default/test-uuid",
			Audience: jwt.Audience{"public"},
			Expiry:   jwt.NewNumericDate(time.Now().Add(1 * time.Hour)),
		},
	}

	token := createTestJWT(t, privateKey, "test-key-id", claims)

	_, err = authenticator.Authenticate(context.Background(), token)
	assert.ErrorIs(t, err, ErrInvalidToken)
	assert.Contains(t, err.Error(), "invalid trust domain")
}

func TestDaprSentryAuthenticator_Authenticate_InvalidSignature(t *testing.T) {
	privateKey, jwk := generateTestKey(t)
	wrongKey, _ := generateTestKey(t)

	jwks := jose.JSONWebKeySet{Keys: []jose.JSONWebKey{jwk}}
	server := setupJWKSServer(t, jwks)

	cfg := DaprSentryConfig{
		Enabled:         true,
		JWKSUrl:         server.URL,
		TrustDomain:     "public",
		RefreshInterval: 5 * time.Minute,
	}

	authenticator, err := NewDaprSentryAuthenticator(context.Background(), cfg)
	require.NoError(t, err)

	claims := daprSentryClaims{
		Claims: jwt.Claims{
			Subject:  "spiffe://public/ns/default/test-uuid",
			Audience: jwt.Audience{"public"},
			Expiry:   jwt.NewNumericDate(time.Now().Add(1 * time.Hour)),
		},
	}

	// Sign with wrong key
	_ = privateKey // Not used - we sign with wrongKey instead
	token := createTestJWT(t, wrongKey, "test-key-id", claims)

	_, err = authenticator.Authenticate(context.Background(), token)
	assert.ErrorIs(t, err, ErrInvalidToken)
	assert.Contains(t, err.Error(), "failed to verify JWT signature")
}

func TestDaprSentryAuthenticator_Authenticate_MissingSubject(t *testing.T) {
	privateKey, jwk := generateTestKey(t)

	jwks := jose.JSONWebKeySet{Keys: []jose.JSONWebKey{jwk}}
	server := setupJWKSServer(t, jwks)

	cfg := DaprSentryConfig{
		Enabled:         true,
		JWKSUrl:         server.URL,
		TrustDomain:     "public",
		RefreshInterval: 5 * time.Minute,
	}

	authenticator, err := NewDaprSentryAuthenticator(context.Background(), cfg)
	require.NoError(t, err)

	claims := daprSentryClaims{
		Claims: jwt.Claims{
			// No subject
			Audience: jwt.Audience{"public"},
			Expiry:   jwt.NewNumericDate(time.Now().Add(1 * time.Hour)),
		},
	}

	token := createTestJWT(t, privateKey, "test-key-id", claims)

	_, err = authenticator.Authenticate(context.Background(), token)
	assert.ErrorIs(t, err, ErrInvalidToken)
	assert.Contains(t, err.Error(), "missing subject claim")
}

func TestDaprSentryAuthenticator_Authenticate_InvalidSPIFFEID(t *testing.T) {
	privateKey, jwk := generateTestKey(t)

	jwks := jose.JSONWebKeySet{Keys: []jose.JSONWebKey{jwk}}
	server := setupJWKSServer(t, jwks)

	cfg := DaprSentryConfig{
		Enabled:         true,
		JWKSUrl:         server.URL,
		TrustDomain:     "public",
		RefreshInterval: 5 * time.Minute,
	}

	authenticator, err := NewDaprSentryAuthenticator(context.Background(), cfg)
	require.NoError(t, err)

	claims := daprSentryClaims{
		Claims: jwt.Claims{
			Subject:  "not-a-spiffe-id", // Invalid SPIFFE ID format
			Audience: jwt.Audience{"public"},
			Expiry:   jwt.NewNumericDate(time.Now().Add(1 * time.Hour)),
		},
	}

	token := createTestJWT(t, privateKey, "test-key-id", claims)

	_, err = authenticator.Authenticate(context.Background(), token)
	assert.ErrorIs(t, err, ErrInvalidToken)
	assert.Contains(t, err.Error(), "invalid trust domain")
}

func TestDaprSentryAuthenticator_Mode(t *testing.T) {
	privateKey, jwk := generateTestKey(t)
	_ = privateKey

	jwks := jose.JSONWebKeySet{Keys: []jose.JSONWebKey{jwk}}
	server := setupJWKSServer(t, jwks)

	cfg := DaprSentryConfig{
		Enabled:         true,
		JWKSUrl:         server.URL,
		TrustDomain:     "public",
		RefreshInterval: 5 * time.Minute,
	}

	authenticator, err := NewDaprSentryAuthenticator(context.Background(), cfg)
	require.NoError(t, err)

	assert.Equal(t, ModeDaprSentry, authenticator.Mode())
}

func TestDaprSentryAuthenticator_JWKSRefresh(t *testing.T) {
	privateKey, jwk := generateTestKey(t)

	fetchCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fetchCount++
		jwks := jose.JSONWebKeySet{Keys: []jose.JSONWebKey{jwk}}
		w.Header().Set("Content-Type", "application/json")
		err := json.NewEncoder(w).Encode(jwks)
		require.NoError(t, err)
	}))
	t.Cleanup(server.Close)

	cfg := DaprSentryConfig{
		Enabled:         true,
		JWKSUrl:         server.URL,
		TrustDomain:     "public",
		RefreshInterval: 1 * time.Millisecond, // Very short for testing
	}

	authenticator, err := NewDaprSentryAuthenticator(context.Background(), cfg)
	require.NoError(t, err)

	// First fetch happens during creation
	assert.Equal(t, 1, fetchCount)

	// Wait for refresh interval to pass
	time.Sleep(10 * time.Millisecond)

	claims := daprSentryClaims{
		Claims: jwt.Claims{
			Subject:  "spiffe://public/ns/default/test-uuid",
			Audience: jwt.Audience{"public"},
			Expiry:   jwt.NewNumericDate(time.Now().Add(1 * time.Hour)),
		},
	}

	token := createTestJWT(t, privateKey, "test-key-id", claims)

	// This should trigger a refresh
	_, err = authenticator.Authenticate(context.Background(), token)
	require.NoError(t, err)

	// Should have fetched JWKS again
	assert.GreaterOrEqual(t, fetchCount, 2)
}

func TestDaprSentryAuthenticator_NoAudienceValidation(t *testing.T) {
	privateKey, jwk := generateTestKey(t)

	jwks := jose.JSONWebKeySet{Keys: []jose.JSONWebKey{jwk}}
	server := setupJWKSServer(t, jwks)

	cfg := DaprSentryConfig{
		Enabled:         true,
		JWKSUrl:         server.URL,
		TrustDomain:     "public",
		Audience:        "", // No audience validation
		RefreshInterval: 5 * time.Minute,
	}

	authenticator, err := NewDaprSentryAuthenticator(context.Background(), cfg)
	require.NoError(t, err)

	claims := daprSentryClaims{
		Claims: jwt.Claims{
			Subject:  "spiffe://public/ns/default/test-uuid",
			Audience: jwt.Audience{"any-audience"},
			Expiry:   jwt.NewNumericDate(time.Now().Add(1 * time.Hour)),
		},
	}

	token := createTestJWT(t, privateKey, "test-key-id", claims)

	identity, err := authenticator.Authenticate(context.Background(), token)
	require.NoError(t, err)
	assert.Equal(t, "spiffe://public/ns/default/test-uuid", identity.Subject)
}

func TestIsValidSPIFFEID(t *testing.T) {
	tests := []struct {
		name        string
		subject     string
		trustDomain string
		expected    bool
	}{
		{
			name:        "valid SPIFFE ID",
			subject:     "spiffe://public/ns/default/test-uuid",
			trustDomain: "public",
			expected:    true,
		},
		{
			name:        "valid SPIFFE ID with cluster.local",
			subject:     "spiffe://cluster.local/ns/default/test-uuid",
			trustDomain: "cluster.local",
			expected:    true,
		},
		{
			name:        "wrong trust domain",
			subject:     "spiffe://other/ns/default/test-uuid",
			trustDomain: "public",
			expected:    false,
		},
		{
			name:        "not a SPIFFE ID",
			subject:     "https://example.com/user",
			trustDomain: "public",
			expected:    false,
		},
		{
			name:        "empty subject",
			subject:     "",
			trustDomain: "public",
			expected:    false,
		},
		{
			name:        "malformed SPIFFE ID",
			subject:     "spiffe://",
			trustDomain: "public",
			expected:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := isValidSPIFFEID(tt.subject, tt.trustDomain)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestContainsAudience(t *testing.T) {
	tests := []struct {
		name      string
		audiences jwt.Audience
		expected  string
		result    bool
	}{
		{
			name:      "contains audience",
			audiences: jwt.Audience{"aud1", "aud2", "aud3"},
			expected:  "aud2",
			result:    true,
		},
		{
			name:      "does not contain audience",
			audiences: jwt.Audience{"aud1", "aud2"},
			expected:  "aud3",
			result:    false,
		},
		{
			name:      "empty audiences",
			audiences: jwt.Audience{},
			expected:  "aud1",
			result:    false,
		},
		{
			name:      "single audience match",
			audiences: jwt.Audience{"public"},
			expected:  "public",
			result:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := containsAudience(tt.audiences, tt.expected)
			assert.Equal(t, tt.result, result)
		})
	}
}

func TestDaprSentryConfig_Validate(t *testing.T) {
	tests := []struct {
		name    string
		config  DaprSentryConfig
		wantErr bool
		errMsg  string
	}{
		{
			name: "valid config",
			config: DaprSentryConfig{
				Enabled:     true,
				JWKSUrl:     "http://sentry:8080/jwks.json",
				TrustDomain: "public",
				Audience:    "public",
			},
			wantErr: false,
		},
		{
			name: "disabled config is valid",
			config: DaprSentryConfig{
				Enabled: false,
			},
			wantErr: false,
		},
		{
			name: "missing JWKS URL",
			config: DaprSentryConfig{
				Enabled:     true,
				TrustDomain: "public",
			},
			wantErr: true,
			errMsg:  "DAPR_SENTRY_JWKS_URL is required",
		},
		{
			name: "missing trust domain",
			config: DaprSentryConfig{
				Enabled: true,
				JWKSUrl: "http://sentry:8080/jwks.json",
			},
			wantErr: true,
			errMsg:  "DAPR_SENTRY_TRUST_DOMAIN is required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.config.Validate()
			if tt.wantErr {
				assert.Error(t, err)
				assert.Contains(t, err.Error(), tt.errMsg)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}
