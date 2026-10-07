package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"time"

	"github.com/dapr/dapr-mcp-server/pkg/health"
)

const (
	healthCheckTimeout     = 3 * time.Second
	defaultHealthCheckAddr = "localhost:8080"
	healthCheckHost        = "localhost"
	exitCodeOK             = 0
	exitCodeFailure        = 1
)

// healthCheckMain runs --health-check and returns the process exit code,
// writing any failure to errOut.
func healthCheckMain(errOut io.Writer) int {
	ctx, cancel := context.WithTimeout(context.Background(), healthCheckTimeout)
	defer cancel()

	target := healthCheckTarget(*healthCheckAddr, *httpAddr)
	if err := runHealthCheck(ctx, http.DefaultClient, target); err != nil {
		_, _ = fmt.Fprintf(errOut, "health check failed: %v\n", err)
		return exitCodeFailure
	}
	return exitCodeOK
}

// healthCheckTarget picks the address to probe: the explicit flag if set,
// else the --http listen address with a wildcard host replaced by localhost,
// else defaultHealthCheckAddr.
func healthCheckTarget(explicitAddr, listenAddr string) string {
	if explicitAddr != "" {
		return explicitAddr
	}
	if listenAddr == "" {
		return defaultHealthCheckAddr
	}
	host, port, err := net.SplitHostPort(listenAddr)
	if err != nil {
		return listenAddr
	}
	if ip := net.ParseIP(host); host == "" || (ip != nil && ip.IsUnspecified()) {
		host = healthCheckHost
	}
	return net.JoinHostPort(host, port)
}

// runHealthCheck probes the liveness endpoint of the server at addr (host:port).
func runHealthCheck(ctx context.Context, client *http.Client, addr string) error {
	target := url.URL{Scheme: "http", Host: addr, Path: health.LivenessPath}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return fmt.Errorf("build health check request: %w", err)
	}
	resp, err := client.Do(req) //nolint:gosec // URL comes from the operator's own flags, not request input
	if err != nil {
		return fmt.Errorf("health check request: %w", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("health check returned status %d", resp.StatusCode)
	}
	return nil
}
