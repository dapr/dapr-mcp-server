package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	dapr "github.com/dapr/go-sdk/client"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"

	actor "github.com/dapr/dapr-mcp-server/pkg/actors"
	"github.com/dapr/dapr-mcp-server/pkg/auth"
	binding "github.com/dapr/dapr-mcp-server/pkg/bindings"
	conversation "github.com/dapr/dapr-mcp-server/pkg/conversation"
	crypto "github.com/dapr/dapr-mcp-server/pkg/crypto"
	"github.com/dapr/dapr-mcp-server/pkg/health"
	invoke "github.com/dapr/dapr-mcp-server/pkg/invoke"
	lock "github.com/dapr/dapr-mcp-server/pkg/lock"
	metadata "github.com/dapr/dapr-mcp-server/pkg/metadata"
	pubsub "github.com/dapr/dapr-mcp-server/pkg/pubsub"
	secret "github.com/dapr/dapr-mcp-server/pkg/secrets"
	state "github.com/dapr/dapr-mcp-server/pkg/state"
	"github.com/dapr/dapr-mcp-server/pkg/telemetry"
)

const (
	daprClientMaxAttempts = 5
	daprClientRetryDelay  = 2 * time.Second
	readHeaderTimeout     = 10 * time.Second
	httpShutdownTimeout   = 15 * time.Second
	telemetryFlushTimeout = 10 * time.Second
	healthCheckURL        = "http://localhost:8080/livez"
)

var (
	// Version is set at build time via -ldflags
	Version = "dev"

	httpAddr    = flag.String("http", "", "if set, use streamable HTTP at this address, instead of stdin/stdout")
	healthCheck = flag.Bool("health-check", false, "run a health check against the running server and exit")
	DaprClient  dapr.Client
)

func main() {
	flag.Parse()

	if *healthCheck {
		if err := runHealthCheck(); err != nil {
			os.Exit(1)
		}
		os.Exit(0)
	}

	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: telemetry.ParseLogLevel(os.Getenv("DAPR_MCP_SERVER_LOG_LEVEL")),
	}))
	slog.SetDefault(logger)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	err := run(ctx, logger)
	stop()
	if err != nil {
		slog.Error("Server failed", "error", err)
		os.Exit(1)
	}
}

// runHealthCheck probes the liveness endpoint of a locally running server.
func runHealthCheck() error {
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, healthCheckURL, nil)
	if err != nil {
		return fmt.Errorf("build health check request: %w", err)
	}
	resp, err := http.DefaultClient.Do(req) //nolint:gosec // health check against localhost only
	if err != nil {
		return fmt.Errorf("health check request: %w", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("health check returned status %d", resp.StatusCode)
	}
	return nil
}

// run wires up the server and blocks until ctx is canceled or the server fails.
// Returning an error instead of calling os.Exit lets the deferred cleanup run.
func run(ctx context.Context, logger *slog.Logger) error {
	logger.Info("Starting dapr-mcp-server", "version", Version)

	shutdownTelemetry, err := telemetry.Initialize(ctx)
	if err != nil {
		logger.Warn("Failed to initialize telemetry, continuing without observability", "error", err)
	} else {
		defer func() {
			// ctx is already canceled by the time we get here on a signal,
			// so flush with a fresh context.
			flushCtx, cancel := context.WithTimeout(context.Background(), telemetryFlushTimeout)
			defer cancel()
			if flushErr := shutdownTelemetry(flushCtx); flushErr != nil {
				slog.Error("Error shutting down telemetry", "error", flushErr)
			}
		}()
		// Switch to the OTEL-wrapped logger that was set as default
		logger = slog.Default()
		logger.Info("OpenTelemetry initialized successfully")
	}

	toolMetrics, err := telemetry.NewToolMetrics()
	if err != nil {
		logger.Warn("Failed to initialize tool metrics", "error", err)
	}

	httpMetrics, err := telemetry.NewHTTPMetrics()
	if err != nil {
		logger.Warn("Failed to initialize HTTP metrics", "error", err)
	}

	prop := propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}, propagation.Baggage{})
	otel.SetTextMapPropagator(prop)

	if err = initializeDaprClient(ctx, logger); err != nil {
		return fmt.Errorf("initialize dapr client: %w", err)
	}
	defer DaprClient.Close()

	instructions := buildInstructions()
	logger.Debug("Server instructions configured", "instructions", instructions)

	server := mcp.NewServer(&mcp.Implementation{Name: "dapr-mcp-server", Version: Version}, &mcp.ServerOptions{
		Instructions: instructions,
		HasTools:     true,
	})

	if err = registerTools(ctx, server, toolMetrics, logger); err != nil {
		return fmt.Errorf("register tools: %w", err)
	}

	if *httpAddr == "" {
		t := &mcp.LoggingTransport{Transport: &mcp.StdioTransport{}, Writer: os.Stderr}
		if err = server.Run(ctx, t); err != nil {
			return fmt.Errorf("stdio server: %w", err)
		}
		return nil
	}

	healthChecker := health.NewHandler(DaprClient, Version)
	handler, err := buildHTTPHandler(ctx, server, healthChecker, httpMetrics, logger)
	if err != nil {
		return err
	}
	return serveHTTP(ctx, *httpAddr, handler, healthChecker, logger)
}

func initializeDaprClient(ctx context.Context, logger *slog.Logger) error {
	for attempt := 1; ; attempt++ {
		client, err := dapr.NewClient()
		if err == nil {
			DaprClient = client
			logger.Info("Dapr client established successfully")
			return nil
		}
		if attempt == daprClientMaxAttempts {
			return fmt.Errorf("failed to create Dapr client after %d attempts: %w", attempt, err)
		}
		logger.Warn("Dapr client initialization failed",
			"attempt", attempt,
			"max_attempts", daprClientMaxAttempts,
			"error", err,
		)

		select {
		case <-ctx.Done():
			return fmt.Errorf("waiting to retry Dapr client: %w", ctx.Err())
		case <-time.After(daprClientRetryDelay):
		}
	}
}

func buildInstructions() string {
	var b strings.Builder
	b.WriteString("You are an expert AI assistant for Dapr microservices. Your role is to translate user requests into precise, deterministic, and safe Dapr MCP tool calls.\n\n")
	b.WriteString("### Global Safety Rules\n")
	b.WriteString("- **Clarity Before Acting**: If ANY required argument is missing (store name, key, topic, etc.), you **MUST run the get_components tool to enrich the information before proceeding**. If arguments are still missing first try the tool with sensible defaults, if this fails ask the user for clarification.\n")
	b.WriteString("- **Serialization**: Metadata fields MUST be a dictionary/map (e.g., `{}`) and NEVER a quoted string (e.g., `\"{}\"`).\n")
	b.WriteString("- **Multi-Step Workflow**: When multiple operations are requested, execute them sequentially — **one tool call at a time**.\n")
	b.WriteString("- **Forbidden Actions**: NEVER invent component names, keys, topics, or cryptographic parameters.\n\n")
	b.WriteString("### Tool Call Validity\n")
	b.WriteString("Consult the tool's Description for specific component rules (e.g., key formatting, security warnings).\n")
	return b.String()
}

// registerTools registers the core tools and then the tools for each
// building block that has at least one component loaded in the sidecar.
func registerTools(ctx context.Context, server *mcp.Server, toolMetrics *telemetry.ToolMetrics, logger *slog.Logger) error {
	metadata.RegisterTools(server, DaprClient, toolMetrics)
	invoke.RegisterTools(server, DaprClient, toolMetrics)
	actor.RegisterTools(server, DaprClient, toolMetrics)

	components, err := metadata.GetLiveComponentList(ctx, DaprClient)
	if err != nil {
		return fmt.Errorf("get components: %w", err)
	}

	present := make(map[string]bool)
	for _, c := range components {
		switch {
		case strings.HasPrefix(c.Type, "state."):
			present["state"] = true
		case strings.HasPrefix(c.Type, "pubsub."):
			present["pubsub"] = true
		case strings.HasPrefix(c.Type, "bindings."):
			present["bindings"] = true
		case strings.HasPrefix(c.Type, "secretstores."):
			present["secrets"] = true
		case strings.HasPrefix(c.Type, "lock."):
			present["lock"] = true
		case strings.HasPrefix(c.Type, "conversation."):
			present["conversation"] = true
		case strings.HasPrefix(c.Type, "crypto."):
			present["crypto"] = true
		}
	}
	logger.Info("Discovered Dapr components", "components", present)

	if present["pubsub"] {
		pubsub.RegisterTools(server, DaprClient, toolMetrics)
	}
	if present["bindings"] {
		binding.RegisterTools(server, DaprClient, toolMetrics)
	}
	if present["state"] {
		state.RegisterTools(server, DaprClient, toolMetrics)
	}
	if present["secrets"] {
		secret.RegisterTools(server, DaprClient, toolMetrics)
	}
	if present["conversation"] {
		conversation.RegisterTools(server, DaprClient, toolMetrics)
	}
	if present["crypto"] {
		crypto.RegisterTools(server, DaprClient, toolMetrics)
	}
	if present["lock"] {
		lock.RegisterTools(server, DaprClient, toolMetrics)
	}
	return nil
}

func corsMiddleware(next http.Handler) http.Handler {
	origin := os.Getenv("DAPR_MCP_CORS_ORIGIN")
	if origin == "" {
		origin = "*"
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", origin)
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, Mcp-Session-Id, Mcp-Protocol-Version")
		w.Header().Set("Access-Control-Expose-Headers", "Mcp-Session-Id, Mcp-Protocol-Version")

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		next.ServeHTTP(w, r)
	})
}

func buildHTTPHandler(ctx context.Context, server *mcp.Server, healthChecker *health.Handler, httpMetrics *telemetry.HTTPMetrics, logger *slog.Logger) (http.Handler, error) {
	authConfig := auth.DefaultConfig()
	if err := authConfig.Validate(); err != nil {
		return nil, fmt.Errorf("invalid authentication configuration: %w", err)
	}

	authMiddleware := auth.NoopMiddleware
	if authConfig.Enabled && authConfig.Mode != auth.ModeDisabled {
		logger.Info("Starting authentication initialization", "mode", authConfig.Mode)
		authenticators, err := buildAuthenticators(ctx, authConfig, logger)
		if err != nil {
			return nil, fmt.Errorf("initialize authenticators: %w", err)
		}
		authMiddleware = auth.NewMiddleware(authConfig, authenticators, logger).Handler
		logger.Info("Authentication enabled",
			"mode", authConfig.Mode,
			"skip_paths", authConfig.SkipPaths,
		)
	} else {
		logger.Info("Authentication disabled")
	}

	mux := http.NewServeMux()

	// Health endpoints sit outside the auth middleware.
	mux.HandleFunc("/livez", healthChecker.LivenessHandler)
	mux.HandleFunc("/readyz", healthChecker.ReadinessHandler)
	mux.HandleFunc("/startupz", healthChecker.StartupHandler)

	mux.HandleFunc("/dapr/subscribe", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[]`))
	})

	mcpHandler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{})
	// Telemetry is the outer layer so metrics cover every request, including auth failures.
	mux.Handle("/", telemetry.HTTPMiddleware(authMiddleware(mcpHandler), logger, httpMetrics))

	logger.Info("MCP HTTP server configured", "auth_enabled", authConfig.Enabled)
	return corsMiddleware(mux), nil
}

// serveHTTP serves until ctx is canceled, then marks the server not ready
// and drains in-flight requests before returning.
func serveHTTP(ctx context.Context, addr string, handler http.Handler, healthChecker *health.Handler, logger *slog.Logger) error {
	// Streaming responses stay open until the client leaves, so cancel every
	// request context once shutdown starts rather than let them hold Shutdown
	// until its timeout.
	baseCtx, cancelRequests := context.WithCancel(context.Background())
	defer cancelRequests()

	srv := &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: readHeaderTimeout,
		BaseContext:       func(net.Listener) context.Context { return baseCtx },
	}
	srv.RegisterOnShutdown(cancelRequests)

	errCh := make(chan error, 1)
	go func() {
		logger.Info("MCP HTTP server starting", "address", addr)
		errCh <- srv.ListenAndServe()
	}()

	select {
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("http server: %w", err)
	case <-ctx.Done():
	}

	logger.Info("Shutdown signal received, draining connections")
	healthChecker.SetReady(false)

	shutdownCtx, cancel := context.WithTimeout(context.Background(), httpShutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("http shutdown: %w", err)
	}
	logger.Info("Server stopped gracefully")
	return nil
}

// buildAuthenticators creates the appropriate authenticators based on configuration.
func buildAuthenticators(ctx context.Context, cfg auth.Config, logger *slog.Logger) ([]auth.Authenticator, error) {
	var authenticators []auth.Authenticator

	switch cfg.Mode {
	case auth.ModeOIDC:
		logger.Info("Initializing OIDC authenticator",
			"issuer_url", cfg.OIDC.IssuerURL,
			"client_id", cfg.OIDC.ClientID,
		)
		oidc, err := auth.NewOIDCAuthenticator(ctx, cfg.OIDC)
		if err != nil {
			return nil, fmt.Errorf("failed to create OIDC authenticator: %w", err)
		}
		authenticators = append(authenticators, oidc)

	case auth.ModeSPIFFE:
		logger.Info("Initializing SPIFFE authenticator",
			"trust_domain", cfg.SPIFFE.TrustDomain,
			"server_id", cfg.SPIFFE.ServerID,
			"endpoint_socket", cfg.SPIFFE.EndpointSocket,
		)
		spiffe, err := auth.NewSPIFFEAuthenticator(ctx, cfg.SPIFFE)
		if err != nil {
			return nil, fmt.Errorf("failed to create SPIFFE authenticator: %w", err)
		}
		authenticators = append(authenticators, spiffe)

	case auth.ModeDaprSentry:
		logger.Info("Initializing Dapr Sentry authenticator",
			"jwks_url", cfg.DaprSentry.JWKSUrl,
			"trust_domain", cfg.DaprSentry.TrustDomain,
			"audience", cfg.DaprSentry.Audience,
			"token_header", cfg.DaprSentry.TokenHeader,
		)
		sentry, err := auth.NewDaprSentryAuthenticatorWithLogger(ctx, cfg.DaprSentry, logger)
		if err != nil {
			return nil, fmt.Errorf("failed to create Dapr Sentry authenticator: %w", err)
		}
		authenticators = append(authenticators, sentry)

	case auth.ModeHybrid:
		if cfg.OIDC.Enabled {
			logger.Info("Initializing OIDC authenticator (hybrid mode)",
				"issuer_url", cfg.OIDC.IssuerURL,
				"client_id", cfg.OIDC.ClientID,
			)
			oidc, err := auth.NewOIDCAuthenticator(ctx, cfg.OIDC)
			if err != nil {
				return nil, fmt.Errorf("failed to create OIDC authenticator: %w", err)
			}
			authenticators = append(authenticators, oidc)
		}
		if cfg.SPIFFE.Enabled {
			logger.Info("Initializing SPIFFE authenticator (hybrid mode)",
				"trust_domain", cfg.SPIFFE.TrustDomain,
				"server_id", cfg.SPIFFE.ServerID,
				"endpoint_socket", cfg.SPIFFE.EndpointSocket,
			)
			spiffe, err := auth.NewSPIFFEAuthenticator(ctx, cfg.SPIFFE)
			if err != nil {
				return nil, fmt.Errorf("failed to create SPIFFE authenticator: %w", err)
			}
			authenticators = append(authenticators, spiffe)
		}
		if cfg.DaprSentry.Enabled {
			logger.Info("Initializing Dapr Sentry authenticator (hybrid mode)",
				"jwks_url", cfg.DaprSentry.JWKSUrl,
				"trust_domain", cfg.DaprSentry.TrustDomain,
				"audience", cfg.DaprSentry.Audience,
				"token_header", cfg.DaprSentry.TokenHeader,
			)
			sentry, err := auth.NewDaprSentryAuthenticatorWithLogger(ctx, cfg.DaprSentry, logger)
			if err != nil {
				return nil, fmt.Errorf("failed to create Dapr Sentry authenticator: %w", err)
			}
			authenticators = append(authenticators, sentry)
		}
	}

	logger.Info("Authenticators initialized", "count", len(authenticators))
	return authenticators, nil
}
