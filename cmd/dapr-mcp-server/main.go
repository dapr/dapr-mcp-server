package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
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

var (
	// Version is set at build time via -ldflags
	Version = "dev"

	httpAddr    = flag.String("http", "", "if set, use streamable HTTP at this address, instead of stdin/stdout")
	healthCheck = flag.Bool("health-check", false, "run a health check against the running server and exit")
	DaprClient  dapr.Client
)

func initializeDaprClient(ctx context.Context, logger *slog.Logger) error {
	const maxRetries = 5
	const retryDelay = 2 * time.Second

	var err error

	for i := 0; i < maxRetries; i++ {
		DaprClient, err = dapr.NewClient()
		if err == nil {
			logger.Info("Dapr client established successfully")
			return nil
		}
		logger.Warn("Dapr client initialization failed",
			"attempt", i+1,
			"max_retries", maxRetries,
			"error", err,
		)

		if i < maxRetries-1 {
			time.Sleep(retryDelay)
		}
	}
	return fmt.Errorf("failed to create Dapr client after %d attempts: %w", maxRetries, err)
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

func main() {
	flag.Parse()

	// Health check mode: probe the running server and exit
	if *healthCheck {
		req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://localhost:8080/livez", nil)
		if err != nil {
			os.Exit(1)
		}
		resp, err := http.DefaultClient.Do(req) //nolint:gosec // health check against localhost only
		if err != nil {
			os.Exit(1)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			os.Exit(1)
		}
		os.Exit(0)
	}

	// Initialize structured logging
	logLevel := os.Getenv("DAPR_MCP_SERVER_LOG_LEVEL")
	var level slog.Level
	switch strings.ToUpper(logLevel) {
	case "DEBUG":
		level = slog.LevelDebug
	case "WARN":
		level = slog.LevelWarn
	case "ERROR":
		level = slog.LevelError
	default:
		level = slog.LevelInfo
	}
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level}))
	slog.SetDefault(logger)

	logger.Info("Starting dapr-mcp-server",
		"version", Version,
	)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Initialize OpenTelemetry
	shutdown, err := telemetry.Initialize(ctx)
	if err != nil {
		logger.Warn("Failed to initialize telemetry, continuing without observability",
			"error", err,
		)
	} else {
		defer func() {
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if shutdownErr := shutdown(shutdownCtx); shutdownErr != nil {
				slog.Error("Error shutting down telemetry", "error", shutdownErr)
			}
		}()
		// Switch to the OTEL-wrapped logger that was set as default
		logger = slog.Default()
		logger.Info("OpenTelemetry initialized successfully")
	}

	// Initialize tool metrics
	toolMetrics, err := telemetry.NewToolMetrics()
	if err != nil {
		logger.Warn("Failed to initialize tool metrics", "error", err)
	}

	// Initialize HTTP metrics
	httpMetrics, err := telemetry.NewHTTPMetrics()
	if err != nil {
		logger.Warn("Failed to initialize HTTP metrics", "error", err)
	}

	// Set up OpenTelemetry propagator for trace context and baggage
	prop := propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}, propagation.Baggage{})
	otel.SetTextMapPropagator(prop)

	// Initialize Dapr client
	if initErr := initializeDaprClient(ctx, logger); initErr != nil {
		logger.Error("Fatal error: could not initialize Dapr client", "error", initErr)
		os.Exit(1)
	}
	defer DaprClient.Close()

	// Build server instructions
	var instructions strings.Builder
	instructions.WriteString("You are an expert AI assistant for Dapr microservices. Your role is to translate user requests into precise, deterministic, and safe Dapr MCP tool calls.\n\n")

	instructions.WriteString("### Global Safety Rules\n")
	instructions.WriteString("- **Clarity Before Acting**: If ANY required argument is missing (store name, key, topic, etc.), you **MUST run the get_components tool to enrich the information before proceeding**. If arguments are still missing first try the tool with sensible defaults, if this fails ask the user for clarification.\n")
	instructions.WriteString("- **Serialization**: Metadata fields MUST be a dictionary/map (e.g., `{}`) and NEVER a quoted string (e.g., `\"{}\"`).\n")
	instructions.WriteString("- **Multi-Step Workflow**: When multiple operations are requested, execute them sequentially — **one tool call at a time**.\n")
	instructions.WriteString("- **Forbidden Actions**: NEVER invent component names, keys, topics, or cryptographic parameters.\n\n")
	instructions.WriteString("### Tool Call Validity\n")
	instructions.WriteString("Consult the tool's Description for specific component rules (e.g., key formatting, security warnings).\n")

	opts := &mcp.ServerOptions{
		Instructions: instructions.String(),
		HasTools:     true,
	}
	logger.Debug("Server instructions configured", "instructions", instructions.String())

	server := mcp.NewServer(&mcp.Implementation{Name: "dapr-mcp-server", Version: Version}, opts)

	// Register core tools
	metadata.RegisterTools(server, DaprClient, toolMetrics)
	invoke.RegisterTools(server, DaprClient, toolMetrics)
	actor.RegisterTools(server, DaprClient, toolMetrics)

	// Discover components and register conditional tools
	componentPresence := make(map[string]bool)
	components, err := metadata.GetLiveComponentList(ctx, DaprClient)
	if err != nil {
		logger.Error("Fatal error: could not get components", "error", err)
		os.Exit(1)
	}
	for _, comp := range components {
		if strings.HasPrefix(comp.Type, "state.") {
			componentPresence["state"] = true
		} else if strings.HasPrefix(comp.Type, "pubsub.") {
			componentPresence["pubsub"] = true
		} else if strings.HasPrefix(comp.Type, "bindings.") {
			componentPresence["bindings"] = true
		} else if strings.HasPrefix(comp.Type, "secretstores.") {
			componentPresence["secrets"] = true
		} else if strings.HasPrefix(comp.Type, "lock.") {
			componentPresence["lock"] = true
		} else if strings.HasPrefix(comp.Type, "conversation.") {
			componentPresence["conversation"] = true
		} else if strings.HasPrefix(comp.Type, "crypto.") {
			componentPresence["crypto"] = true
		}
	}

	logger.Info("Discovered Dapr components", "components", componentPresence)

	if componentPresence["pubsub"] {
		pubsub.RegisterTools(server, DaprClient, toolMetrics)
	}
	if componentPresence["bindings"] {
		binding.RegisterTools(server, DaprClient, toolMetrics)
	}
	if componentPresence["state"] {
		state.RegisterTools(server, DaprClient, toolMetrics)
	}
	if componentPresence["secrets"] {
		secret.RegisterTools(server, DaprClient, toolMetrics)
	}
	if componentPresence["conversation"] {
		conversation.RegisterTools(server, DaprClient, toolMetrics)
	}
	if componentPresence["crypto"] {
		crypto.RegisterTools(server, DaprClient, toolMetrics)
	}
	if componentPresence["lock"] {
		lock.RegisterTools(server, DaprClient, toolMetrics)
	}

	if *httpAddr != "" {
		// Initialize health checker
		healthChecker := health.NewHandler(DaprClient, Version)

		// Initialize authentication
		authConfig := auth.DefaultConfig()
		if err := authConfig.Validate(); err != nil {
			logger.Error("Invalid authentication configuration", "error", err)
			os.Exit(1)
		}

		var authMiddleware func(http.Handler) http.Handler
		if authConfig.Enabled && authConfig.Mode != auth.ModeDisabled {
			logger.Info("Starting authentication initialization", "mode", authConfig.Mode)
			authenticators, err := buildAuthenticators(ctx, authConfig, logger)
			if err != nil {
				logger.Error("Failed to initialize authenticators", "error", err)
				os.Exit(1)
			}
			middleware := auth.NewMiddleware(authConfig, authenticators, logger)
			authMiddleware = middleware.Handler
			logger.Info("Authentication enabled",
				"mode", authConfig.Mode,
				"skip_paths", authConfig.SkipPaths,
			)
		} else {
			authMiddleware = auth.NoopMiddleware
			logger.Info("Authentication disabled")
		}

		// Create HTTP mux for health endpoints
		mux := http.NewServeMux()

		// Register health endpoints (no auth required)
		mux.HandleFunc("/livez", healthChecker.LivenessHandler)
		mux.HandleFunc("/readyz", healthChecker.ReadinessHandler)
		mux.HandleFunc("/startupz", healthChecker.StartupHandler)

		// Create MCP Streamable HTTP handler
		mcpHandler := mcp.NewStreamableHTTPHandler(func(request *http.Request) *mcp.Server {
			return server
		}, &mcp.StreamableHTTPOptions{})

		// Wrap with telemetry (outer) and auth (inner) middleware
		// Telemetry must be outer so metrics are recorded for ALL requests including auth failures
		wrappedMCPHandler := telemetry.HTTPMiddleware(authMiddleware(mcpHandler), logger, httpMetrics)

		// Handle Dapr subscription endpoint
		mux.HandleFunc("/dapr/subscribe", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[]`))
		})

		// Route all other requests to MCP handler
		mux.Handle("/", wrappedMCPHandler)

		srv := &http.Server{
			Addr:              *httpAddr,
			Handler:           corsMiddleware(mux),
			ReadHeaderTimeout: 10 * time.Second,
		}

		go func() {
			logger.Info("MCP HTTP server starting",
				"address", *httpAddr,
				"auth_enabled", authConfig.Enabled,
			)
			if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				logger.Error("Server failed", "error", err)
				os.Exit(1)
			}
		}()

		<-ctx.Done()
		logger.Info("Shutdown signal received, draining connections...")

		shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			logger.Error("Server shutdown error", "error", err)
		}
		logger.Info("Server stopped gracefully")
	} else {
		t := &mcp.LoggingTransport{Transport: &mcp.StdioTransport{}, Writer: os.Stderr}
		if err := server.Run(ctx, t); err != nil {
			logger.Error("Server failed", "error", err)
			os.Exit(1)
		}
	}
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
