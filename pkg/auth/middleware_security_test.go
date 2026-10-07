package auth

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stubAuthenticator accepts exactly one token.
type stubAuthenticator struct{ valid string }

func (s stubAuthenticator) Authenticate(_ context.Context, token string) (*Identity, error) {
	if token != s.valid {
		return nil, ErrInvalidToken
	}
	return &Identity{Subject: "user", AuthMethod: ModeOIDC}, nil
}

func (stubAuthenticator) Mode() AuthMode { return ModeOIDC }

func TestParseAuthorization(t *testing.T) {
	t.Parallel()
	tests := []struct {
		value      string
		wantToken  string
		wantHeader string
	}{
		{value: "Bearer abc", wantToken: "abc", wantHeader: "Authorization (Bearer)"},
		{value: "bearer abc", wantToken: "abc", wantHeader: "Authorization (Bearer)"},
		{value: "BEARER abc", wantToken: "abc", wantHeader: "Authorization (Bearer)"},
		{value: "abc", wantToken: "abc", wantHeader: "Authorization (raw)"},
		{value: ""},
		{value: "Bearer"},
		{value: "Bearer "},
		{value: "Bearer  abc"},
		{value: "Bearer abc def"},
		{value: "Bearer abc\tdef"},
		{value: "Basic dXNlcjpwYXNz"},
		{value: "abc\tdef"},
	}
	for _, tt := range tests {
		t.Run(tt.value, func(t *testing.T) {
			t.Parallel()
			token, header := parseAuthorization(tt.value)
			assert.Equal(t, tt.wantToken, token)
			assert.Equal(t, tt.wantHeader, header)
		})
	}
}

func TestExtractTokenCustomHeader(t *testing.T) {
	t.Parallel()
	m := &Middleware{config: Config{DaprSentry: DaprSentryConfig{TokenHeader: "X-My-Auth"}}}
	tests := []struct {
		name       string
		headers    http.Header
		wantToken  string
		wantHeader string
	}{
		{
			name:       "present but empty falls back to Authorization",
			headers:    http.Header{"X-My-Auth": {""}, "Authorization": {"Bearer fallback"}},
			wantToken:  "fallback",
			wantHeader: "Authorization (Bearer)",
		},
		{
			name:       "whitespace only falls back to Authorization",
			headers:    http.Header{"X-My-Auth": {"   "}, "Authorization": {"Bearer fallback"}},
			wantToken:  "fallback",
			wantHeader: "Authorization (Bearer)",
		},
		{
			name:    "embedded whitespace rejected",
			headers: http.Header{"X-My-Auth": {"abc def"}, "Authorization": {"Bearer fallback"}},
		},
		{
			name:       "surrounding whitespace trimmed",
			headers:    http.Header{"X-My-Auth": {" abc "}},
			wantToken:  "abc",
			wantHeader: "X-My-Auth",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req.Header = tt.headers
			token, header := m.extractTokenWithSource(req)
			assert.Equal(t, tt.wantToken, token)
			assert.Equal(t, tt.wantHeader, header)
		})
	}
}

func TestShouldSkipRejectsUncleanPaths(t *testing.T) {
	t.Parallel()
	m := &Middleware{config: Config{SkipPaths: []string{"/livez", "/api/*"}}}
	tests := []struct {
		path string
		want bool
	}{
		{path: "/livez", want: true},
		{path: "/api/users", want: true},
		{path: "/api/users/", want: true},
		{path: "", want: false},
		{path: "//livez", want: false},
		{path: "/livez/", want: false},
		{path: "/livez/../mcp", want: false},
		{path: "/api/../mcp", want: false},
		{path: "/api//users", want: false},
		{path: "/api/./users", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, m.shouldSkip(tt.path))
		})
	}
}

func TestMiddlewareNotBypassedThroughServeMux(t *testing.T) {
	t.Parallel()
	const validToken = "good-token"
	protectedReached := false

	mux := http.NewServeMux()
	mux.HandleFunc("/livez", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	mux.HandleFunc("/livez/", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	mux.HandleFunc("/mcp", func(w http.ResponseWriter, _ *http.Request) {
		protectedReached = true
		w.WriteHeader(http.StatusOK)
	})

	cfg := Config{Mode: ModeOIDC, SkipPaths: []string{"/livez", "/livez/*"}}
	require.NoError(t, validateSkipPaths(cfg.SkipPaths))
	handler := NewMiddleware(cfg, []Authenticator{stubAuthenticator{valid: validToken}}, nil).Handler(mux)
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	get := func(t *testing.T, rawPath, token string) int {
		t.Helper()
		req, err := http.NewRequest(http.MethodGet, server.URL, nil)
		require.NoError(t, err)
		req.URL.Path = rawPath
		if token != "" {
			req.Header.Set(authorizationHeader, bearerScheme+" "+token)
		}
		resp, err := client.Do(req)
		require.NoError(t, err)
		_ = resp.Body.Close()
		return resp.StatusCode
	}

	assert.Equal(t, http.StatusOK, get(t, "/livez", ""))
	for _, p := range []string{"/livez/../mcp", "//livez/../mcp", "/livez/./../mcp", "//mcp"} {
		t.Run(p, func(t *testing.T) {
			assert.Equal(t, http.StatusUnauthorized, get(t, p, ""))
		})
	}
	assert.False(t, protectedReached, "protected handler reached without a token")

	redirect := get(t, "/livez/../mcp", validToken)
	assert.True(t, redirect >= http.StatusMultipleChoices && redirect < http.StatusBadRequest,
		"ServeMux redirects unclean paths, got %d", redirect)
	assert.Equal(t, http.StatusOK, get(t, "/mcp", validToken))
	assert.True(t, protectedReached)
}

func TestMiddlewareRedactsSensitiveHeaders(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	cfg := Config{Mode: ModeOIDC, DaprSentry: DaprSentryConfig{TokenHeader: "X-My-Auth"}}
	handler := NewMiddleware(cfg, []Authenticator{stubAuthenticator{valid: "custom-secret"}}, logger).
		Handler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))

	secrets := map[string]string{
		"Authorization":       "Bearer auth-secret",
		"proxy-authorization": "Basic proxy-secret",
		"COOKIE":              "session=cookie-secret",
		"Set-Cookie":          "set-cookie-secret",
		"x-api-key":           "api-key-secret",
	}
	req := httptest.NewRequest(http.MethodGet, "/mcp", nil)
	for k, v := range secrets {
		req.Header[k] = []string{v}
	}
	req.Header.Set("x-my-auth", "custom-secret")
	req.Header.Set("X-Request-Id", "visible-value")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	logs := buf.String()
	for _, secret := range []string{"auth-secret", "proxy-secret", "cookie-secret", "set-cookie-secret", "api-key-secret", "custom-secret"} {
		assert.NotContains(t, logs, secret)
	}
	assert.Contains(t, logs, redactedValue)
	assert.Contains(t, logs, "visible-value")
}

func TestMiddlewareRejectsMalformedBearer(t *testing.T) {
	t.Parallel()
	handler := NewMiddleware(Config{Mode: ModeOIDC}, []Authenticator{stubAuthenticator{valid: "tok"}}, nil).
		Handler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))

	for _, value := range []string{"Bearer ", "Bearer  tok", "Bearer tok extra", "Basic tok"} {
		t.Run(value, func(t *testing.T) {
			t.Parallel()
			req := httptest.NewRequest(http.MethodGet, "/mcp", nil)
			req.Header.Set(authorizationHeader, value)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			assert.Equal(t, http.StatusUnauthorized, rec.Code)
		})
	}
}

func TestStubAuthenticatorRejects(t *testing.T) {
	t.Parallel()
	_, err := stubAuthenticator{valid: "a"}.Authenticate(context.Background(), "b")
	assert.True(t, errors.Is(err, ErrInvalidToken))
}
