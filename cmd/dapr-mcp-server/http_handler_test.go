package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/dapr/dapr-mcp-server/pkg/auth"
	"github.com/dapr/dapr-mcp-server/pkg/health"
)

const emptyJWKS = `{"keys":[]}`

func newReadyHealthHandler() *health.Handler {
	h := health.NewHandler(nil, "test", discardLogger())
	h.SetStartupDone(true)
	h.SetReady(true)
	return h
}

func newTestMCPServer() *mcp.Server {
	return mcp.NewServer(&mcp.Implementation{Name: "test", Version: "v0.0.0"}, nil)
}

// clearAuthEnv makes the auth configuration independent of the developer's environment.
func clearAuthEnv(t *testing.T) {
	t.Helper()
	for _, key := range []string{
		"AUTH_ENABLED", "AUTH_MODE", "AUTH_SKIP_PATHS",
		"OIDC_ENABLED", "SPIFFE_ENABLED", "DAPR_SENTRY_ENABLED",
		"DAPR_SENTRY_JWKS_URL", "DAPR_SENTRY_TRUST_DOMAIN", "DAPR_SENTRY_AUDIENCE",
		corsOriginEnv,
	} {
		t.Setenv(key, "")
	}
}

func enableSentryAuth(t *testing.T, jwksURL string) {
	t.Helper()
	t.Setenv("AUTH_ENABLED", "true")
	t.Setenv("AUTH_MODE", string(auth.ModeDaprSentry))
	t.Setenv("DAPR_SENTRY_JWKS_URL", jwksURL)
	t.Setenv("DAPR_SENTRY_TRUST_DOMAIN", "example.test")
}

func newJWKSServer(t *testing.T, status int) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(emptyJWKS))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func serve(handler http.Handler, method, path string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(method, path, nil))
	return rec
}

// These tests use t.Setenv, so they cannot run in parallel.

func TestBuildHTTPHandlerAuthDisabled(t *testing.T) {
	clearAuthEnv(t)

	handler, err := buildHTTPHandler(context.Background(), newTestMCPServer(), newReadyHealthHandler(), nil, discardLogger())
	require.NoError(t, err)

	for _, path := range []string{health.LivenessPath, health.ReadinessPath, health.StartupPath} {
		assert.Equal(t, http.StatusOK, serve(handler, http.MethodGet, path).Code, path)
	}
	assert.NotEqual(t, http.StatusUnauthorized, serve(handler, http.MethodPost, "/").Code, "no auth in disabled mode")
	assert.Empty(t, serve(handler, http.MethodGet, health.LivenessPath).Header().Get("Access-Control-Allow-Origin"))
}

func TestBuildHTTPHandlerHealthBypassesAuth(t *testing.T) {
	clearAuthEnv(t)
	enableSentryAuth(t, newJWKSServer(t, http.StatusOK).URL)

	handler, err := buildHTTPHandler(context.Background(), newTestMCPServer(), newReadyHealthHandler(), nil, discardLogger())
	require.NoError(t, err)

	for _, path := range []string{health.LivenessPath, health.ReadinessPath, health.StartupPath} {
		assert.Equal(t, http.StatusOK, serve(handler, http.MethodGet, path).Code, path)
	}
	assert.Equal(t, http.StatusUnauthorized, serve(handler, http.MethodPost, "/").Code, "MCP endpoint requires a token")
}

func TestBuildHTTPHandlerHybridSentry(t *testing.T) {
	clearAuthEnv(t)
	enableSentryAuth(t, newJWKSServer(t, http.StatusOK).URL)
	t.Setenv("AUTH_MODE", string(auth.ModeHybrid))
	t.Setenv("DAPR_SENTRY_ENABLED", "true")

	handler, err := buildHTTPHandler(context.Background(), newTestMCPServer(), newReadyHealthHandler(), nil, discardLogger())
	require.NoError(t, err)
	assert.Equal(t, http.StatusUnauthorized, serve(handler, http.MethodPost, "/").Code)
}

func TestBuildHTTPHandlerCORSFromEnv(t *testing.T) {
	clearAuthEnv(t)
	t.Setenv(corsOriginEnv, testOrigin)

	handler, err := buildHTTPHandler(context.Background(), newTestMCPServer(), newReadyHealthHandler(), nil, discardLogger())
	require.NoError(t, err)

	assert.Equal(t, testOrigin, serve(handler, http.MethodGet, health.LivenessPath).Header().Get("Access-Control-Allow-Origin"))
}

func TestBuildHTTPHandlerErrors(t *testing.T) {
	tests := []struct {
		name    string
		jwksURL func(t *testing.T) string
		wantErr string
	}{
		{
			name:    "invalid auth configuration",
			jwksURL: func(*testing.T) string { return "" },
			wantErr: "invalid authentication configuration",
		},
		{
			name:    "authenticator initialization fails",
			jwksURL: func(t *testing.T) string { return newJWKSServer(t, http.StatusInternalServerError).URL },
			wantErr: "initialize authenticators",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clearAuthEnv(t)
			enableSentryAuth(t, tt.jwksURL(t))

			handler, err := buildHTTPHandler(context.Background(), newTestMCPServer(), newReadyHealthHandler(), nil, discardLogger())
			assert.ErrorContains(t, err, tt.wantErr)
			assert.Nil(t, handler)
		})
	}
}

func TestBuildInstructionsMentionsGetComponents(t *testing.T) {
	t.Parallel()
	assert.Contains(t, buildInstructions(), "get_components")
}
