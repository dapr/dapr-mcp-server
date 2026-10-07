package main

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/dapr/dapr-mcp-server/pkg/health"
)

func TestHealthCheckTarget(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		explicit string
		listen   string
		want     string
	}{
		{name: "explicit wins", explicit: "example:9000", listen: "0.0.0.0:8080", want: "example:9000"},
		{name: "default without http", want: defaultHealthCheckAddr},
		{name: "ipv4 wildcard", listen: "0.0.0.0:9090", want: "localhost:9090"},
		{name: "ipv6 wildcard", listen: "[::]:9090", want: "localhost:9090"},
		{name: "empty host", listen: ":9090", want: "localhost:9090"},
		{name: "specific host kept", listen: "127.0.0.1:9090", want: "127.0.0.1:9090"},
		{name: "unparseable passed through", listen: "not-an-addr", want: "not-an-addr"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, healthCheckTarget(tt.explicit, tt.listen))
		})
	}
}

func serverAddr(t *testing.T, srv *httptest.Server) string {
	t.Helper()
	u, err := url.Parse(srv.URL)
	require.NoError(t, err)
	return u.Host
}

func TestRunHealthCheck(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		status  int
		wantErr bool
	}{
		{name: "ok", status: http.StatusOK},
		{name: "server error", status: http.StatusInternalServerError, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var gotPath string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotPath = r.URL.Path
				w.WriteHeader(tt.status)
			}))
			t.Cleanup(srv.Close)

			err := runHealthCheck(context.Background(), srv.Client(), serverAddr(t, srv))

			assert.Equal(t, health.LivenessPath, gotPath)
			if tt.wantErr {
				assert.ErrorContains(t, err, "status 500")
				return
			}
			assert.NoError(t, err)
		})
	}
}

func TestRunHealthCheckConnectionRefused(t *testing.T) {
	t.Parallel()
	// Bind and release a port so nothing is listening on it.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := ln.Addr().String()
	require.NoError(t, ln.Close())

	err = runHealthCheck(context.Background(), http.DefaultClient, addr)
	assert.ErrorContains(t, err, "health check request")
}
