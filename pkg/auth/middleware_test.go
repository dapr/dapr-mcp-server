package auth

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

// MockAuthenticator is a mock implementation of Authenticator for testing
type MockAuthenticator struct {
	mock.Mock
}

func (m *MockAuthenticator) Authenticate(ctx context.Context, token string) (*Identity, error) {
	args := m.Called(ctx, token)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*Identity), args.Error(1)
}

func (m *MockAuthenticator) Mode() AuthMode {
	args := m.Called()
	return args.Get(0).(AuthMode)
}

func TestNewMiddleware(t *testing.T) {
	cfg := Config{
		Mode: ModeOIDC,
	}

	middleware := NewMiddleware(cfg, nil, nil)

	assert.NotNil(t, middleware)
	assert.Equal(t, cfg.Enabled(), middleware.config.Enabled())
}

func TestNewMiddlewareWithLogger(t *testing.T) {
	cfg := Config{Mode: ModeOIDC}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	middleware := NewMiddleware(cfg, nil, logger)

	assert.NotNil(t, middleware)
	assert.Equal(t, logger, middleware.logger)
}

func TestNewMiddlewareWithNilLogger(t *testing.T) {
	cfg := Config{Mode: ModeOIDC}

	middleware := NewMiddleware(cfg, nil, nil)

	assert.NotNil(t, middleware)
	assert.NotNil(t, middleware.logger)
}

func TestMiddlewareHandler_AuthDisabled(t *testing.T) {
	cfg := Config{
		Mode: ModeDisabled,
	}

	middleware := NewMiddleware(cfg, nil, nil)
	nextHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	handler := middleware.Handler(nextHandler)
	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestMiddlewareHandler_ModeDisabled(t *testing.T) {
	cfg := Config{
		Mode: ModeDisabled,
	}

	middleware := NewMiddleware(cfg, nil, nil)
	nextHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	handler := middleware.Handler(nextHandler)
	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestMiddlewareHandler_NoToken(t *testing.T) {
	cfg := Config{
		Mode: ModeOIDC,
	}

	mockAuth := new(MockAuthenticator)
	middleware := NewMiddleware(cfg, []Authenticator{mockAuth}, nil)

	nextHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	handler := middleware.Handler(nextHandler)
	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.Contains(t, rec.Body.String(), "no token provided")
}

func TestMiddlewareHandler_ValidBearerToken(t *testing.T) {
	cfg := Config{
		Mode: ModeOIDC,
	}

	mockAuth := new(MockAuthenticator)
	mockAuth.On("Mode").Return(ModeOIDC)
	mockAuth.On("Authenticate", mock.Anything, "valid-token").
		Return(&Identity{Subject: "user-123", AuthMethod: ModeOIDC}, nil)

	middleware := NewMiddleware(cfg, []Authenticator{mockAuth}, nil)

	var capturedIdentity *Identity
	nextHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedIdentity = GetIdentity(r.Context())
		w.WriteHeader(http.StatusOK)
	})

	handler := middleware.Handler(nextHandler)
	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	req.Header.Set("Authorization", "Bearer valid-token")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.NotNil(t, capturedIdentity)
	assert.Equal(t, "user-123", capturedIdentity.Subject)
}

func TestMiddlewareHandler_RawToken(t *testing.T) {
	cfg := Config{
		Mode: ModeOIDC,
	}

	mockAuth := new(MockAuthenticator)
	mockAuth.On("Mode").Return(ModeOIDC)
	mockAuth.On("Authenticate", mock.Anything, "raw-token").
		Return(&Identity{Subject: "user-123"}, nil)

	middleware := NewMiddleware(cfg, []Authenticator{mockAuth}, nil)

	nextHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	handler := middleware.Handler(nextHandler)
	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	req.Header.Set("Authorization", "raw-token")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestMiddlewareHandler_CustomTokenHeader(t *testing.T) {
	cfg := Config{
		Mode: ModeDaprSentry,
		DaprSentry: DaprSentryConfig{
			TokenHeader: "X-Dapr-Token",
		},
	}

	mockAuth := new(MockAuthenticator)
	mockAuth.On("Mode").Return(ModeDaprSentry)
	mockAuth.On("Authenticate", mock.Anything, "custom-header-token").
		Return(&Identity{Subject: "dapr-app"}, nil)

	middleware := NewMiddleware(cfg, []Authenticator{mockAuth}, nil)

	nextHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	handler := middleware.Handler(nextHandler)
	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	req.Header.Set("X-Dapr-Token", "custom-header-token")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestMiddlewareHandler_InvalidToken(t *testing.T) {
	cfg := Config{
		Mode: ModeOIDC,
	}

	mockAuth := new(MockAuthenticator)
	mockAuth.On("Mode").Return(ModeOIDC)
	mockAuth.On("Authenticate", mock.Anything, "invalid-token").
		Return(nil, errors.New("token validation failed"))

	middleware := NewMiddleware(cfg, []Authenticator{mockAuth}, nil)

	nextHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	handler := middleware.Handler(nextHandler)
	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	req.Header.Set("Authorization", "Bearer invalid-token")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.Contains(t, rec.Body.String(), "invalid token")
}

func TestMiddlewareHandler_MultipleAuthenticators(t *testing.T) {
	cfg := Config{
		Mode: ModeHybrid,
	}

	mockAuth1 := new(MockAuthenticator)
	mockAuth1.On("Mode").Return(ModeOIDC)
	mockAuth1.On("Authenticate", mock.Anything, "token").
		Return(nil, errors.New("not an OIDC token"))

	mockAuth2 := new(MockAuthenticator)
	mockAuth2.On("Mode").Return(ModeSPIFFE)
	mockAuth2.On("Authenticate", mock.Anything, "token").
		Return(&Identity{Subject: "spiffe://cluster.local/app", AuthMethod: ModeSPIFFE}, nil)

	middleware := NewMiddleware(cfg, []Authenticator{mockAuth1, mockAuth2}, nil)

	nextHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		identity := GetIdentity(r.Context())
		assert.Equal(t, ModeSPIFFE, identity.AuthMethod)
		w.WriteHeader(http.StatusOK)
	})

	handler := middleware.Handler(nextHandler)
	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	req.Header.Set("Authorization", "Bearer token")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestMiddlewareHandler_AllAuthenticatorsFail(t *testing.T) {
	cfg := Config{
		Mode: ModeHybrid,
	}

	mockAuth1 := new(MockAuthenticator)
	mockAuth1.On("Mode").Return(ModeOIDC)
	mockAuth1.On("Authenticate", mock.Anything, "bad-token").
		Return(nil, errors.New("oidc failed"))

	mockAuth2 := new(MockAuthenticator)
	mockAuth2.On("Mode").Return(ModeSPIFFE)
	mockAuth2.On("Authenticate", mock.Anything, "bad-token").
		Return(nil, errors.New("spiffe failed"))

	middleware := NewMiddleware(cfg, []Authenticator{mockAuth1, mockAuth2}, nil)

	nextHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	handler := middleware.Handler(nextHandler)
	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	req.Header.Set("Authorization", "Bearer bad-token")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestExtractTokenWithSource(t *testing.T) {
	tests := []struct {
		name           string
		config         Config
		headers        map[string]string
		expectedToken  string
		expectedHeader string
	}{
		{
			name: "bearer token",
			config: Config{
				DaprSentry: DaprSentryConfig{TokenHeader: "Authorization"},
			},
			headers:        map[string]string{"Authorization": "Bearer my-token"},
			expectedToken:  "my-token",
			expectedHeader: "Authorization (Bearer)",
		},
		{
			name: "raw token in Authorization",
			config: Config{
				DaprSentry: DaprSentryConfig{TokenHeader: "Authorization"},
			},
			headers:        map[string]string{"Authorization": "raw-token"},
			expectedToken:  "raw-token",
			expectedHeader: "Authorization (raw)",
		},
		{
			name: "custom header",
			config: Config{
				DaprSentry: DaprSentryConfig{TokenHeader: "X-Custom-Token"},
			},
			headers:        map[string]string{"X-Custom-Token": "custom-token"},
			expectedToken:  "custom-token",
			expectedHeader: "X-Custom-Token",
		},
		{
			name: "custom header takes priority over Authorization",
			config: Config{
				DaprSentry: DaprSentryConfig{TokenHeader: "X-Custom-Token"},
			},
			headers: map[string]string{
				"X-Custom-Token": "custom-token",
				"Authorization":  "Bearer auth-token",
			},
			expectedToken:  "custom-token",
			expectedHeader: "X-Custom-Token",
		},
		{
			name: "fallback to Authorization when custom header empty",
			config: Config{
				DaprSentry: DaprSentryConfig{TokenHeader: "X-Custom-Token"},
			},
			headers:        map[string]string{"Authorization": "Bearer fallback-token"},
			expectedToken:  "fallback-token",
			expectedHeader: "Authorization (Bearer)",
		},
		{
			name: "no token",
			config: Config{
				DaprSentry: DaprSentryConfig{TokenHeader: "Authorization"},
			},
			headers:        map[string]string{},
			expectedToken:  "",
			expectedHeader: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			middleware := &Middleware{config: tt.config}
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			for k, v := range tt.headers {
				req.Header.Set(k, v)
			}

			token, header := middleware.extractTokenWithSource(req)

			assert.Equal(t, tt.expectedToken, token)
			assert.Equal(t, tt.expectedHeader, header)
		})
	}
}

func TestSafeTokenPrefix(t *testing.T) {
	tests := []struct {
		name     string
		token    string
		expected string
	}{
		{
			name:     "long token",
			token:    "eyJhbGciOiJSUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiJ0ZXN0In0",
			expected: "eyJhbGciOiJSUzI1NiIs...",
		},
		{
			name:     "exactly 20 chars",
			token:    "12345678901234567890",
			expected: "[token too short to preview safely]",
		},
		{
			name:     "short token",
			token:    "short",
			expected: "[token too short to preview safely]",
		},
		{
			name:     "21 chars",
			token:    "123456789012345678901",
			expected: "12345678901234567890...",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := safeTokenPrefix(tt.token)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestNoopMiddleware(t *testing.T) {
	nextHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	})

	handler := NoopMiddleware(nextHandler)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "OK", rec.Body.String())
}

func TestMiddlewareHandler_BearerCaseInsensitive(t *testing.T) {
	cfg := Config{
		Mode: ModeOIDC,
	}

	mockAuth := new(MockAuthenticator)
	mockAuth.On("Mode").Return(ModeOIDC)
	mockAuth.On("Authenticate", mock.Anything, "my-token").
		Return(&Identity{Subject: "user"}, nil)

	middleware := NewMiddleware(cfg, []Authenticator{mockAuth}, nil)

	nextHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	handler := middleware.Handler(nextHandler)
	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	req.Header.Set("Authorization", "bearer my-token") // lowercase bearer
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
}
