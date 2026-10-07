package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	dapr "github.com/dapr/go-sdk/client"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/dapr/dapr-mcp-server/pkg/auth"
	"github.com/dapr/dapr-mcp-server/pkg/health"
	"github.com/dapr/dapr-mcp-server/pkg/telemetry"
)

const (
	daprClientMaxAttempts = 5
	daprClientRetryDelay  = 2 * time.Second
	readHeaderTimeout     = 10 * time.Second
	httpIdleTimeout       = 120 * time.Second
	httpMaxHeaderBytes    = 1 << 20
	httpShutdownTimeout   = 15 * time.Second
	telemetryFlushTimeout = 10 * time.Second
	logLevelEnv           = "DAPR_MCP_SERVER_LOG_LEVEL"
	corsOriginEnv         = "DAPR_MCP_CORS_ORIGIN"
	corsAllowMethods      = "GET, POST, PUT, DELETE, OPTIONS"
	corsAllowHeaders      = "Content-Type, Authorization, Mcp-Session-Id, Mcp-Protocol-Version"
	corsExposeHeaders     = "Mcp-Session-Id, Mcp-Protocol-Version"
)

var (
	// Version is set at build time via -ldflags
	Version = "dev"

	httpAddr        = flag.String("http", "", "if set, use streamable HTTP at this address, instead of stdin/stdout")
	showVersion     = flag.Bool("version", false, "print the version and exit")
	healthCheck     = flag.Bool("health-check", false, "run a health check against the running server and exit")
	healthCheckAddr = flag.String("health-check-addr", "", "host:port probed by --health-check (default: derived from --http, else "+defaultHealthCheckAddr+")")
)

func main() {
	flag.Parse()

	if *showVersion {
		printVersion(os.Stdout)
		return
	}

	if *healthCheck {
		os.Exit(healthCheckMain(os.Stderr))
	}

	logger := newLogger(os.Stderr)
	slog.SetDefault(logger)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	err := run(ctx, logger)
	stop()
	if err != nil {
		slog.Error("Server failed", "error", err)
		os.Exit(1)
	}
}

// runStdio serves MCP over t until ctx is canceled or the client disconnects.
// The transport is not wrapped in a logging transport,
// because that would copy every request and response, secrets included, to stderr.
// Cancellation is the normal signal shutdown, so it is not reported as an error.
func runStdio(ctx context.Context, server *mcp.Server, t mcp.Transport) error {
	if err := server.Run(ctx, t); err != nil && !errors.Is(err, context.Canceled) {
		return fmt.Errorf("stdio server: %w", err)
	}
	return nil
}

func printVersion(w io.Writer) {
	_, _ = fmt.Fprintln(w, Version)
}

// newLogger builds the JSON logger that writes to w.
// main passes os.Stderr because in the stdio transport stdout carries the JSON-RPC stream.
func newLogger(w io.Writer) *slog.Logger {
	return slog.New(slog.NewJSONHandler(w, &slog.HandlerOptions{
		Level: telemetry.ParseLogLevel(os.Getenv(logLevelEnv)),
	}))
}

// run wires up the server and blocks until ctx is canceled or the server fails.
// Returning an error instead of calling os.Exit lets the deferred cleanup run.
func run(ctx context.Context, logger *slog.Logger) error {
	logger.Info("Starting dapr-mcp-server", "version", Version)

	shutdownTelemetry, err := telemetry.Initialize(ctx, telemetry.WithServiceVersion(Version))
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
		// Initialize installs a stderr logger as the default, which also exports records when OTEL log export is on.
		logger = slog.Default()
		logger.Info("OpenTelemetry initialized successfully")
	}

	toolMetrics, err := telemetry.NewToolMetrics()
	if err != nil {
		logger.Warn("Failed to initialize tool metrics, tool calls will run without metrics", "error", err)
	}

	httpMetrics, err := telemetry.NewHTTPMetrics()
	if err != nil {
		logger.Warn("Failed to initialize HTTP metrics, requests will be served without metrics", "error", err)
	}

	daprClient, err := initializeDaprClient(ctx, newDefaultDaprClient, daprClientRetryDelay, logger)
	if err != nil {
		return fmt.Errorf("initialize dapr client: %w", err)
	}
	defer daprClient.Close()

	instructions := buildInstructions()
	logger.Debug("Server instructions configured", "instructions", instructions)

	server := mcp.NewServer(&mcp.Implementation{Name: "dapr-mcp-server", Version: Version}, &mcp.ServerOptions{
		Instructions: instructions,
		HasTools:     true,
	})

	healthChecker := health.NewHandler(daprClient, Version, logger)

	if err = registerTools(ctx, server, daprClient, toolMetrics, logger); err != nil {
		return fmt.Errorf("register tools: %w", err)
	}
	healthChecker.SetStartupDone(true)
	healthChecker.SetReady(true)

	if *httpAddr == "" {
		return runStdio(ctx, server, &mcp.StdioTransport{})
	}

	handler, authenticators, err := buildHTTPHandler(ctx, server, healthChecker, httpMetrics, logger)
	if err != nil {
		return err
	}
	defer func() {
		if err := auth.CloseAuthenticators(authenticators); err != nil {
			logger.Warn("Failed to close authenticators", "error", err)
		}
	}()
	return serveHTTP(ctx, *httpAddr, handler, healthChecker, logger)
}

// daprClientFactory creates a Dapr client. It is a parameter so tests can avoid a real sidecar.
type daprClientFactory func() (dapr.Client, error)

func newDefaultDaprClient() (dapr.Client, error) {
	return dapr.NewClient()
}

// initializeDaprClient calls newClient until it succeeds,
// waiting retryDelay between up to daprClientMaxAttempts attempts.
func initializeDaprClient(ctx context.Context, newClient daprClientFactory, retryDelay time.Duration, logger *slog.Logger) (dapr.Client, error) {
	for attempt := 1; ; attempt++ {
		client, err := newClient()
		if err == nil {
			logger.Info("Dapr client established successfully")
			return client, nil
		}
		if attempt == daprClientMaxAttempts {
			return nil, fmt.Errorf("failed to create Dapr client after %d attempts: %w", attempt, err)
		}
		logger.Warn("Dapr client initialization failed",
			"attempt", attempt,
			"max_attempts", daprClientMaxAttempts,
			"error", err,
		)

		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("waiting to retry Dapr client: %w", ctx.Err())
		case <-time.After(retryDelay):
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

// corsMiddleware adds CORS headers for origin and answers preflight requests.
// An empty origin disables CORS entirely, so browsers on other origins are refused by default.
func corsMiddleware(origin string, next http.Handler) http.Handler {
	if origin == "" {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", origin)
		w.Header().Set("Access-Control-Allow-Methods", corsAllowMethods)
		w.Header().Set("Access-Control-Allow-Headers", corsAllowHeaders)
		w.Header().Set("Access-Control-Expose-Headers", corsExposeHeaders)

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		next.ServeHTTP(w, r)
	})
}

// buildHTTPHandler wires the health, subscription and MCP routes, with auth in front of MCP.
// The returned authenticators hold background resources, so the caller closes them once the server stops.
func buildHTTPHandler(ctx context.Context, server *mcp.Server, healthChecker *health.Handler, httpMetrics *telemetry.HTTPMetrics, logger *slog.Logger) (http.Handler, []auth.Authenticator, error) {
	authConfig := auth.DefaultConfig()
	if err := authConfig.Validate(); err != nil {
		return nil, nil, fmt.Errorf("invalid authentication configuration: %w", err)
	}

	var authenticators []auth.Authenticator
	authMiddleware := auth.NoopMiddleware
	if authConfig.Enabled() {
		logger.Info("Starting authentication initialization", "mode", authConfig.Mode)
		var err error
		authenticators, err = buildAuthenticators(ctx, authConfig, logger)
		if err != nil {
			return nil, nil, fmt.Errorf("initialize authenticators: %w", err)
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
	healthChecker.RegisterHandlers(mux)

	mux.HandleFunc("/dapr/subscribe", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[]`))
	})

	mcpHandler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{})
	// Telemetry is the outer layer so metrics cover every request, including auth failures.
	mux.Handle("/", telemetry.HTTPMiddleware(authMiddleware(mcpHandler), logger, httpMetrics))

	logger.Info("MCP HTTP server configured", "auth_enabled", authConfig.Enabled())
	return corsMiddleware(os.Getenv(corsOriginEnv), mux), authenticators, nil
}

// serveHTTP serves until ctx is canceled, then marks the server not ready
// and drains in-flight requests before returning.
func serveHTTP(ctx context.Context, addr string, handler http.Handler, healthChecker *health.Handler, logger *slog.Logger) error {
	streamsCtx, closeStreams := context.WithCancel(context.Background())
	defer closeStreams()

	// WriteTimeout stays unset: server-sent event streams are long-lived responses.
	srv := &http.Server{
		Addr:              addr,
		Handler:           closeStreamsOnShutdown(streamsCtx, handler),
		ReadHeaderTimeout: readHeaderTimeout,
		IdleTimeout:       httpIdleTimeout,
		MaxHeaderBytes:    httpMaxHeaderBytes,
	}
	srv.RegisterOnShutdown(closeStreams)

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
		if closeErr := srv.Close(); closeErr != nil {
			err = errors.Join(err, closeErr)
		}
		return fmt.Errorf("http shutdown: %w", err)
	}
	logger.Info("Server stopped gracefully")
	return nil
}

// closeStreamsOnShutdown ends GET requests once shutdownCtx is canceled.
// A streamable HTTP GET is a server-sent event stream that stays open until the client leaves,
// so it would otherwise hold Shutdown until its timeout.
// Other requests, such as POSTed tool calls, keep their own context and run to completion.
func closeStreamsOnShutdown(shutdownCtx context.Context, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			next.ServeHTTP(w, r)
			return
		}
		ctx, cancel := context.WithCancel(r.Context())
		defer cancel()
		stop := context.AfterFunc(shutdownCtx, cancel)
		defer stop()
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// buildAuthenticators creates the authenticators the configured mode calls for.
// A single mode enables its own authenticator; hybrid mode enables each one whose flag is set.
// If any authenticator fails to start, the ones already created are closed.
func buildAuthenticators(ctx context.Context, cfg auth.Config, logger *slog.Logger) (authenticators []auth.Authenticator, err error) {
	hybrid := cfg.Mode == auth.ModeHybrid
	defer func() {
		if err != nil {
			err = errors.Join(err, auth.CloseAuthenticators(authenticators))
			authenticators = nil
		}
	}()

	if cfg.Mode == auth.ModeOIDC || (hybrid && cfg.OIDC.Enabled) {
		logger.Info("Initializing OIDC authenticator",
			"issuer_url", cfg.OIDC.IssuerURL,
			"client_id", cfg.OIDC.ClientID,
		)
		oidc, err := auth.NewOIDCAuthenticatorWithLogger(ctx, cfg.OIDC, logger)
		if err != nil {
			return authenticators, fmt.Errorf("create OIDC authenticator: %w", err)
		}
		authenticators = append(authenticators, oidc)
	}

	if cfg.Mode == auth.ModeSPIFFE || (hybrid && cfg.SPIFFE.Enabled) {
		logger.Info("Initializing SPIFFE authenticator",
			"trust_domain", cfg.SPIFFE.TrustDomain,
			"server_id", cfg.SPIFFE.ServerID,
			"endpoint_socket", cfg.SPIFFE.EndpointSocket,
		)
		spiffe, err := auth.NewSPIFFEAuthenticator(ctx, cfg.SPIFFE)
		if err != nil {
			return authenticators, fmt.Errorf("create SPIFFE authenticator: %w", err)
		}
		authenticators = append(authenticators, spiffe)
	}

	if cfg.Mode == auth.ModeDaprSentry || (hybrid && cfg.DaprSentry.Enabled) {
		logger.Info("Initializing Dapr Sentry authenticator",
			"jwks_url", cfg.DaprSentry.JWKSUrl,
			"trust_domain", cfg.DaprSentry.TrustDomain,
			"audience", cfg.DaprSentry.Audience,
			"issuer", cfg.DaprSentry.Issuer,
			"token_header", cfg.DaprSentry.TokenHeader,
		)
		sentry, err := auth.NewDaprSentryAuthenticatorWithLogger(ctx, cfg.DaprSentry, logger)
		if err != nil {
			return authenticators, fmt.Errorf("create Dapr Sentry authenticator: %w", err)
		}
		authenticators = append(authenticators, sentry)
	}

	logger.Info("Authenticators initialized", "count", len(authenticators))
	return authenticators, nil
}
