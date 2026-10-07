package auth

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
	"github.com/spiffe/go-spiffe/v2/spiffeid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	testKeyID       = "test-key-id"
	testTrustDomain = "public"
	testAudience    = "public"
	testSubject     = "spiffe://public/ns/default/17c9f2b4-859d-4249-8701-7e846540e704"
)

// testClock is a manually advanced clock shared by an authenticator and its test.
type testClock struct {
	mu  sync.Mutex
	now time.Time
}

func newTestClock() *testClock { return &testClock{now: time.Now()} }

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *testClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// jwksServer serves a swappable response and counts requests.
type jwksServer struct {
	*httptest.Server
	hits    atomic.Int64
	mu      sync.Mutex
	handler http.HandlerFunc
}

func newJWKSServer(t *testing.T, jwks jose.JSONWebKeySet) *jwksServer {
	t.Helper()
	s := &jwksServer{}
	s.setKeys(jwks)
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.hits.Add(1)
		s.mu.Lock()
		h := s.handler
		s.mu.Unlock()
		h(w, r)
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *jwksServer) setHandler(h http.HandlerFunc) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.handler = h
}

func (s *jwksServer) setKeys(jwks jose.JSONWebKeySet) {
	s.setHandler(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(jwks)
	})
}

func (s *jwksServer) setStatus(code int) {
	s.setHandler(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(code) })
}

func generateTestKey(t *testing.T) (*rsa.PrivateKey, jose.JSONWebKey) {
	t.Helper()
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	return privateKey, jose.JSONWebKey{
		Key:       &privateKey.PublicKey,
		KeyID:     testKeyID,
		Algorithm: string(jose.RS256),
		Use:       jwkUseSignature,
	}
}

func signToken(t *testing.T, alg jose.SignatureAlgorithm, key interface{}, keyID string, claims interface{}) string {
	t.Helper()
	opts := (&jose.SignerOptions{}).WithType("JWT")
	if keyID != "" {
		opts = opts.WithHeader("kid", keyID)
	}
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: alg, Key: key}, opts)
	require.NoError(t, err)
	token, err := jwt.Signed(signer).Claims(claims).Serialize()
	require.NoError(t, err)
	return token
}

func createTestJWT(t *testing.T, privateKey *rsa.PrivateKey, keyID string, claims interface{}) string {
	t.Helper()
	return signToken(t, jose.RS256, privateKey, keyID, claims)
}

func validClaims(now time.Time) daprSentryClaims {
	return daprSentryClaims{
		Claims: jwt.Claims{
			Subject:   testSubject,
			Audience:  jwt.Audience{testAudience},
			Expiry:    jwt.NewNumericDate(now.Add(time.Hour)),
			IssuedAt:  jwt.NewNumericDate(now),
			NotBefore: jwt.NewNumericDate(now.Add(-time.Minute)),
			ID:        "4a4c85636a3328bf0db29a40a6fda63a",
		},
		Use: jwkUseSignature,
	}
}

func testSentryConfig(url string) DaprSentryConfig {
	return DaprSentryConfig{
		Enabled:         true,
		JWKSUrl:         url,
		TrustDomain:     testTrustDomain,
		Audience:        testAudience,
		RefreshInterval: DefaultJWKSRefreshInterval,
	}
}

func newTestSentry(t *testing.T, cfg DaprSentryConfig, clock *testClock) *DaprSentryAuthenticator {
	t.Helper()
	a, err := NewDaprSentryAuthenticator(context.Background(), cfg)
	require.NoError(t, err)
	t.Cleanup(func() { _ = a.Close() })
	if clock != nil {
		// Start the clock after the constructor's fetch so cooldown arithmetic is exact.
		clock.mu.Lock()
		clock.now = time.Now()
		clock.mu.Unlock()
		a.now = clock.Now
	}
	return a
}

func TestDaprSentryAuthenticator_Success(t *testing.T) {
	t.Parallel()
	privateKey, jwk := generateTestKey(t)
	server := newJWKSServer(t, jose.JSONWebKeySet{Keys: []jose.JSONWebKey{jwk}})
	a := newTestSentry(t, testSentryConfig(server.URL), nil)

	identity, err := a.Authenticate(context.Background(), createTestJWT(t, privateKey, testKeyID, validClaims(time.Now())))

	require.NoError(t, err)
	assert.Equal(t, testSubject, identity.Subject)
	assert.Equal(t, ModeDaprSentry, identity.AuthMethod)
	assert.Equal(t, ModeDaprSentry, a.Mode())
	assert.Contains(t, identity.Audience, testAudience)
	assert.Equal(t, jwkUseSignature, identity.Claims["use"])
	assert.Equal(t, "4a4c85636a3328bf0db29a40a6fda63a", identity.Claims["jti"])
	assert.Contains(t, identity.Claims, "exp")
	assert.Contains(t, identity.Claims, "iat")
}

func TestDaprSentryAuthenticator_Claims(t *testing.T) {
	t.Parallel()
	privateKey, jwk := generateTestKey(t)
	server := newJWKSServer(t, jose.JSONWebKeySet{Keys: []jose.JSONWebKey{jwk}})
	const issuer = "https://sentry.example"
	withIssuer := testSentryConfig(server.URL)
	withIssuer.Issuer = issuer
	plain := newTestSentry(t, testSentryConfig(server.URL), nil)
	checksIssuer := newTestSentry(t, withIssuer, nil)

	tests := []struct {
		name    string
		auth    *DaprSentryAuthenticator
		mutate  func(c *daprSentryClaims, now time.Time)
		wantErr error
	}{
		{name: "valid", mutate: func(*daprSentryClaims, time.Time) {}},
		{
			name:    "missing exp",
			mutate:  func(c *daprSentryClaims, _ time.Time) { c.Expiry = nil },
			wantErr: ErrInvalidToken,
		},
		{
			name:    "expired beyond leeway",
			mutate:  func(c *daprSentryClaims, now time.Time) { c.Expiry = jwt.NewNumericDate(now.Add(-2 * clockSkewLeeway)) },
			wantErr: ErrTokenExpired,
		},
		{
			name:   "expired within leeway",
			mutate: func(c *daprSentryClaims, now time.Time) { c.Expiry = jwt.NewNumericDate(now.Add(-clockSkewLeeway / 2)) },
		},
		{
			name: "nbf in the future",
			mutate: func(c *daprSentryClaims, now time.Time) {
				c.NotBefore = jwt.NewNumericDate(now.Add(2 * clockSkewLeeway))
			},
			wantErr: ErrInvalidToken,
		},
		{
			name: "nbf within leeway",
			mutate: func(c *daprSentryClaims, now time.Time) {
				c.NotBefore = jwt.NewNumericDate(now.Add(clockSkewLeeway / 2))
			},
		},
		{
			name: "iat in the future",
			mutate: func(c *daprSentryClaims, now time.Time) {
				c.IssuedAt = jwt.NewNumericDate(now.Add(2 * clockSkewLeeway))
			},
			wantErr: ErrInvalidToken,
		},
		{
			name:    "wrong audience",
			mutate:  func(c *daprSentryClaims, _ time.Time) { c.Audience = jwt.Audience{"other"} },
			wantErr: ErrInvalidAudience,
		},
		{
			name:    "missing audience",
			mutate:  func(c *daprSentryClaims, _ time.Time) { c.Audience = nil },
			wantErr: ErrInvalidAudience,
		},
		{
			name:    "wrong issuer",
			auth:    checksIssuer,
			mutate:  func(c *daprSentryClaims, _ time.Time) { c.Issuer = "https://evil.example" },
			wantErr: ErrInvalidIssuer,
		},
		{
			name:   "matching issuer",
			auth:   checksIssuer,
			mutate: func(c *daprSentryClaims, _ time.Time) { c.Issuer = issuer },
		},
		{
			name:    "missing subject",
			mutate:  func(c *daprSentryClaims, _ time.Time) { c.Subject = "" },
			wantErr: ErrInvalidToken,
		},
		{
			name:    "subject in another trust domain",
			mutate:  func(c *daprSentryClaims, _ time.Time) { c.Subject = "spiffe://other/ns/default/app" },
			wantErr: ErrInvalidToken,
		},
		{
			name:    "subject with userinfo",
			mutate:  func(c *daprSentryClaims, _ time.Time) { c.Subject = "spiffe://public@evil/x" },
			wantErr: ErrInvalidToken,
		},
		{
			name:    "subject with suffixed trust domain",
			mutate:  func(c *daprSentryClaims, _ time.Time) { c.Subject = "spiffe://public.evil.com/x" },
			wantErr: ErrInvalidToken,
		},
		{
			name:    "subject without path",
			mutate:  func(c *daprSentryClaims, _ time.Time) { c.Subject = "spiffe://public" },
			wantErr: ErrInvalidToken,
		},
		{
			name:    "subject not a SPIFFE ID",
			mutate:  func(c *daprSentryClaims, _ time.Time) { c.Subject = "https://example.com/user" },
			wantErr: ErrInvalidToken,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			a := tt.auth
			if a == nil {
				a = plain
			}
			now := time.Now()
			claims := validClaims(now)
			tt.mutate(&claims, now)

			identity, err := a.Authenticate(context.Background(), createTestJWT(t, privateKey, testKeyID, claims))

			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				assert.Nil(t, identity)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, claims.Subject, identity.Subject)
		})
	}
}

func TestDaprSentryAuthenticator_RejectsBadSignatures(t *testing.T) {
	t.Parallel()
	privateKey, jwk := generateTestKey(t)
	otherKey, _ := generateTestKey(t)
	ecKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)

	claims := validClaims(time.Now())
	payload, err := json.Marshal(claims)
	require.NoError(t, err)
	b64 := base64.RawURLEncoding.EncodeToString
	noneToken := b64([]byte(`{"alg":"none","kid":"`+testKeyID+`"}`)) + "." + b64(payload) + "."

	valid := createTestJWT(t, privateKey, testKeyID, claims)
	parts := strings.Split(valid, ".")
	tamperedClaims := claims
	tamperedClaims.Subject = "spiffe://public/ns/default/admin"
	tamperedPayload, err := json.Marshal(tamperedClaims)
	require.NoError(t, err)
	tampered := parts[0] + "." + b64(tamperedPayload) + "." + parts[2]

	ecJWK := jose.JSONWebKey{Key: &ecKey.PublicKey, KeyID: testKeyID, Use: jwkUseSignature}

	tests := []struct {
		name  string
		keys  []jose.JSONWebKey
		token string
	}{
		{name: "alg none", keys: []jose.JSONWebKey{jwk}, token: noneToken},
		{name: "HS256", keys: []jose.JSONWebKey{jwk}, token: signToken(t, jose.HS256, []byte("0123456789abcdef0123456789abcdef"), testKeyID, claims)},
		{name: "tampered payload", keys: []jose.JSONWebKey{jwk}, token: tampered},
		{name: "signed by another key", keys: []jose.JSONWebKey{jwk}, token: createTestJWT(t, otherKey, testKeyID, claims)},
		{name: "RSA token against EC key", keys: []jose.JSONWebKey{ecJWK}, token: valid},
		{name: "garbage", keys: []jose.JSONWebKey{jwk}, token: "not.a.jwt"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			server := newJWKSServer(t, jose.JSONWebKeySet{Keys: tt.keys})
			a := newTestSentry(t, testSentryConfig(server.URL), nil)

			_, err := a.Authenticate(context.Background(), tt.token)

			require.ErrorIs(t, err, ErrInvalidToken)
		})
	}
}

func TestDaprSentryAuthenticator_KeySelection(t *testing.T) {
	t.Parallel()
	privateKey, jwk := generateTestKey(t)
	_, decoy := generateTestKey(t)
	decoy.KeyID = "decoy"

	wrongAlg := jwk
	wrongAlg.Algorithm = string(jose.RS512)
	encUse := jwk
	encUse.Use = "enc"
	noMeta := jwk
	noMeta.Algorithm = ""
	noMeta.Use = ""

	tests := []struct {
		name    string
		keys    []jose.JSONWebKey
		kid     string
		wantErr bool
	}{
		{name: "empty kid tries every key", keys: []jose.JSONWebKey{decoy, jwk}},
		{name: "kid selects key", keys: []jose.JSONWebKey{decoy, jwk}, kid: testKeyID},
		{name: "key without alg or use", keys: []jose.JSONWebKey{noMeta}, kid: testKeyID},
		{name: "key alg differs from header", keys: []jose.JSONWebKey{wrongAlg}, kid: testKeyID, wantErr: true},
		{name: "encryption key", keys: []jose.JSONWebKey{encUse}, kid: testKeyID, wantErr: true},
		{name: "unknown kid", keys: []jose.JSONWebKey{decoy}, kid: testKeyID, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			server := newJWKSServer(t, jose.JSONWebKeySet{Keys: tt.keys})
			a := newTestSentry(t, testSentryConfig(server.URL), nil)

			_, err := a.Authenticate(context.Background(), createTestJWT(t, privateKey, tt.kid, validClaims(time.Now())))

			if tt.wantErr {
				require.ErrorIs(t, err, ErrInvalidToken)
				return
			}
			require.NoError(t, err)
		})
	}
}

func TestCandidateKeys(t *testing.T) {
	t.Parallel()
	_, jwk := generateTestKey(t)
	other := jwk
	other.KeyID = "other"
	other.Algorithm = string(jose.ES256)

	set := &jose.JSONWebKeySet{Keys: []jose.JSONWebKey{jwk, other}}

	assert.Nil(t, candidateKeys(nil, testKeyID, string(jose.RS256)))
	assert.Len(t, candidateKeys(set, "", string(jose.RS256)), 1)
	assert.Len(t, candidateKeys(set, "other", string(jose.ES256)), 1)
	assert.Empty(t, candidateKeys(set, "other", string(jose.RS256)))
	assert.Empty(t, candidateKeys(set, "missing", string(jose.RS256)))
}

func TestDaprSentryAuthenticator_UnknownKidRefreshIsShared(t *testing.T) {
	t.Parallel()
	privateKey, jwk := generateTestKey(t)
	server := newJWKSServer(t, jose.JSONWebKeySet{Keys: []jose.JSONWebKey{jwk}})
	clock := newTestClock()
	a := newTestSentry(t, testSentryConfig(server.URL), clock)
	require.EqualValues(t, 1, server.hits.Load())

	token := createTestJWT(t, privateKey, "unknown-kid", validClaims(clock.Now()))

	_, err := a.Authenticate(context.Background(), token)
	require.ErrorIs(t, err, ErrSigningKeyNotFound)
	assert.EqualValues(t, 1, server.hits.Load(), "unknown kid within cooldown must not fetch")

	clock.Advance(MinJWKSRefreshInterval)

	const callers = 25
	var wg sync.WaitGroup
	for range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := a.Authenticate(context.Background(), token)
			assert.ErrorIs(t, err, ErrSigningKeyNotFound)
		}()
	}
	wg.Wait()

	assert.EqualValues(t, 2, server.hits.Load(), "concurrent unknown kids must share one fetch")
}

func TestDaprSentryAuthenticator_RotatedKeyPickedUpAfterCooldown(t *testing.T) {
	t.Parallel()
	_, jwk := generateTestKey(t)
	newKey, newJWK := generateTestKey(t)
	newJWK.KeyID = "rotated"
	server := newJWKSServer(t, jose.JSONWebKeySet{Keys: []jose.JSONWebKey{jwk}})
	clock := newTestClock()
	a := newTestSentry(t, testSentryConfig(server.URL), clock)

	server.setKeys(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{jwk, newJWK}})
	clock.Advance(MinJWKSRefreshInterval)

	_, err := a.Authenticate(context.Background(), createTestJWT(t, newKey, "rotated", validClaims(clock.Now())))

	require.NoError(t, err)
	assert.EqualValues(t, 2, server.hits.Load())
}

func TestDaprSentryAuthenticator_PeriodicRefresh(t *testing.T) {
	t.Parallel()
	privateKey, jwk := generateTestKey(t)
	server := newJWKSServer(t, jose.JSONWebKeySet{Keys: []jose.JSONWebKey{jwk}})
	clock := newTestClock()
	a := newTestSentry(t, testSentryConfig(server.URL), clock)

	authenticate := func() error {
		_, err := a.Authenticate(context.Background(), createTestJWT(t, privateKey, testKeyID, validClaims(clock.Now())))
		return err
	}

	require.NoError(t, authenticate())
	assert.EqualValues(t, 1, server.hits.Load(), "fresh cache must not fetch")

	clock.Advance(DefaultJWKSRefreshInterval + time.Second)
	require.NoError(t, authenticate())
	assert.EqualValues(t, 2, server.hits.Load(), "due cache must refresh")

	// A failing Sentry is retried at most once per cooldown and the cached keys keep working.
	server.setStatus(http.StatusServiceUnavailable)
	clock.Advance(DefaultJWKSRefreshInterval + time.Second)
	require.NoError(t, authenticate())
	require.NoError(t, authenticate())
	assert.EqualValues(t, 3, server.hits.Load(), "failed refresh must start the cooldown")

	clock.Advance(MinJWKSRefreshInterval)
	require.NoError(t, authenticate())
	assert.EqualValues(t, 4, server.hits.Load(), "refresh retried after cooldown")

	clock.Advance(maxJWKSStaleness)
	err := authenticate()
	require.ErrorIs(t, err, ErrJWKSStale)
	require.ErrorIs(t, err, ErrInvalidToken)
}

func TestDaprSentryAuthenticator_NewRejectsBadConfig(t *testing.T) {
	t.Parallel()
	_, jwk := generateTestKey(t)
	server := newJWKSServer(t, jose.JSONWebKeySet{Keys: []jose.JSONWebKey{jwk}})

	tests := []struct {
		name    string
		mutate  func(*DaprSentryConfig)
		wantErr error
	}{
		{name: "missing audience", mutate: func(c *DaprSentryConfig) { c.Audience = "" }, wantErr: ErrSentryAudienceRequired},
		{name: "missing JWKS URL", mutate: func(c *DaprSentryConfig) { c.JWKSUrl = "" }, wantErr: ErrSentryJWKSURLRequired},
		{name: "missing trust domain", mutate: func(c *DaprSentryConfig) { c.TrustDomain = "" }, wantErr: ErrSentryTrustDomainRequired},
		{name: "invalid trust domain", mutate: func(c *DaprSentryConfig) { c.TrustDomain = "Not A Domain!" }},
		{name: "refresh too short", mutate: func(c *DaprSentryConfig) { c.RefreshInterval = time.Millisecond }, wantErr: ErrSentryRefreshInterval},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cfg := testSentryConfig(server.URL)
			tt.mutate(&cfg)

			a, err := NewDaprSentryAuthenticator(context.Background(), cfg)

			require.Error(t, err)
			assert.Nil(t, a)
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
			}
		})
	}
}

func TestDaprSentryAuthenticator_ZeroRefreshIntervalUsesDefault(t *testing.T) {
	t.Parallel()
	_, jwk := generateTestKey(t)
	server := newJWKSServer(t, jose.JSONWebKeySet{Keys: []jose.JSONWebKey{jwk}})
	cfg := testSentryConfig(server.URL)
	cfg.RefreshInterval = 0

	a := newTestSentry(t, cfg, nil)

	assert.Equal(t, DefaultJWKSRefreshInterval, a.config.RefreshInterval)
}

func TestDaprSentryAuthenticator_FetchErrors(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		handler http.HandlerFunc
		errMsg  string
	}{
		{
			name:    "non-200",
			handler: func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusInternalServerError) },
			errMsg:  "status 500",
		},
		{
			name: "oversized body",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(strings.Repeat(" ", maxJWKSResponseBytes+1)))
			},
			errMsg: "exceeds",
		},
		{
			name:    "invalid JSON",
			handler: func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("{not json")) },
			errMsg:  "parse JWKS",
		},
		{
			name:    "empty key set",
			handler: func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{"keys":[]}`)) },
			errMsg:  "no keys",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			server := newJWKSServer(t, jose.JSONWebKeySet{})
			server.setHandler(tt.handler)

			_, err := NewDaprSentryAuthenticator(context.Background(), testSentryConfig(server.URL))

			require.ErrorContains(t, err, tt.errMsg)
		})
	}
}

func TestDaprSentryAuthenticator_RefreshHonoursContextAndTimeout(t *testing.T) {
	t.Parallel()
	_, jwk := generateTestKey(t)
	server := newJWKSServer(t, jose.JSONWebKeySet{Keys: []jose.JSONWebKey{jwk}})
	a := newTestSentry(t, testSentryConfig(server.URL), nil)

	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	server.setHandler(func(_ http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.ErrorIs(t, a.refreshJWKS(ctx), context.Canceled)

	a.httpClient.Timeout = 50 * time.Millisecond
	err := a.refreshJWKS(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Timeout")

	assert.NotNil(t, a.snapshot().jwks, "failed refresh keeps the cached keys")
}

func TestDaprSentryAuthenticator_ConcurrentAuthenticateAndRefresh(t *testing.T) {
	t.Parallel()
	privateKey, jwk := generateTestKey(t)
	server := newJWKSServer(t, jose.JSONWebKeySet{Keys: []jose.JSONWebKey{jwk}})
	a := newTestSentry(t, testSentryConfig(server.URL), nil)
	token := createTestJWT(t, privateKey, testKeyID, validClaims(time.Now()))

	var wg sync.WaitGroup
	for i := range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if i%4 == 0 {
				assert.NoError(t, a.refreshJWKS(context.Background()))
				return
			}
			_, err := a.Authenticate(context.Background(), token)
			assert.NoError(t, err)
		}()
	}
	wg.Wait()
}

func TestParseSPIFFESubject(t *testing.T) {
	t.Parallel()
	td := spiffeid.RequireTrustDomainFromString(testTrustDomain)
	tests := []struct {
		subject string
		valid   bool
	}{
		{subject: "spiffe://public/ns/default/app", valid: true},
		{subject: ""},
		{subject: "spiffe://"},
		{subject: "spiffe:///x"},
		{subject: "spiffe://public"},
		{subject: "spiffe://other/ns/default/app"},
		{subject: "spiffe://public@evil/x"},
		{subject: "spiffe://public.evil.com/x"},
		{subject: "SPIFFE://public/x"},
		{subject: "https://public/x"},
	}
	for _, tt := range tests {
		t.Run(tt.subject, func(t *testing.T) {
			t.Parallel()
			id, err := parseSPIFFESubject(tt.subject, td)
			if tt.valid {
				require.NoError(t, err)
				assert.Equal(t, tt.subject, id.String())
				return
			}
			require.Error(t, err)
		})
	}
}

func TestSafeTokenPreview(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "header=1 chars, payload=2 chars, sig=3 chars", safeTokenPreview("a.bb.ccc"))
	assert.Equal(t, "[invalid JWT format: 1 parts]", safeTokenPreview("abc"))
}
