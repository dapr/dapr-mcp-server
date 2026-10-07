package main

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/dapr/dapr-mcp-server/pkg/health"
)

func TestServeHTTPStopsOnContextCancel(t *testing.T) {
	t.Parallel()
	healthChecker := health.NewHandler(nil, "test", discardLogger())
	healthChecker.SetReady(true)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)

	go func() {
		done <- serveHTTP(ctx, "127.0.0.1:0", http.NotFoundHandler(), healthChecker, discardLogger())
	}()
	cancel()

	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(httpShutdownTimeout):
		t.Fatal("serveHTTP did not return after the context was canceled")
	}

	rec := httptest.NewRecorder()
	healthChecker.ReadinessHandler(rec, httptest.NewRequest(http.MethodGet, health.ReadinessPath, nil))
	assert.Equal(t, http.StatusServiceUnavailable, rec.Code, "server must report not ready while draining")
}

func TestServeHTTPBindError(t *testing.T) {
	t.Parallel()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })

	healthChecker := health.NewHandler(nil, "test", discardLogger())
	err = serveHTTP(context.Background(), ln.Addr().String(), http.NotFoundHandler(), healthChecker, discardLogger())

	assert.ErrorContains(t, err, "http server:")
}
