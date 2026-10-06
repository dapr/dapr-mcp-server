// Package telemetry provides OpenTelemetry initialization and configuration.
package telemetry

import (
	"context"
	"log/slog"
	"os"
	"strings"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploggrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploghttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	sdkresource "go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// Config holds the telemetry configuration.
type Config struct {
	ServiceName    string
	ServiceVersion string
	Endpoint       string
	Protocol       string // "grpc" or "http/protobuf"
	Headers        map[string]string
	MetricsEnabled bool
	LogsEnabled    bool
}

// Telemetry holds the telemetry providers.
type Telemetry struct {
	TracerProvider *sdktrace.TracerProvider
	MeterProvider  *sdkmetric.MeterProvider
	LoggerProvider *sdklog.LoggerProvider
	Logger         *slog.Logger
	shutdown       []func(context.Context) error
}

// DefaultConfig returns a configuration from environment variables.
func DefaultConfig() Config {
	protocol := os.Getenv("OTEL_EXPORTER_OTLP_TRACES_PROTOCOL")
	if protocol == "" {
		protocol = os.Getenv("OTEL_EXPORTER_OTLP_PROTOCOL")
	}
	if protocol == "" {
		protocol = "grpc"
	}

	endpoint := os.Getenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT")
	if endpoint == "" {
		endpoint = os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT")
	}

	headersStr := os.Getenv("OTEL_EXPORTER_OTLP_HEADERS")
	headers := parseHeaders(headersStr)

	serviceName := os.Getenv("OTEL_SERVICE_NAME")
	if serviceName == "" {
		serviceName = "dapr-mcp-server"
	}

	serviceVersion := os.Getenv("OTEL_SERVICE_VERSION")
	if serviceVersion == "" {
		serviceVersion = "v1.0.0"
	}

	metricsEnabled := os.Getenv("DAPR_MCP_SERVER_METRICS_ENABLED") != "false"
	logsEnabled := os.Getenv("DAPR_MCP_SERVER_LOGS_OTEL_ENABLED") != "false"

	return Config{
		ServiceName:    serviceName,
		ServiceVersion: serviceVersion,
		Endpoint:       endpoint,
		Protocol:       protocol,
		Headers:        headers,
		MetricsEnabled: metricsEnabled,
		LogsEnabled:    logsEnabled,
	}
}

// parseHeaders parses the OTEL_EXPORTER_OTLP_HEADERS format.
func parseHeaders(headersStr string) map[string]string {
	headers := make(map[string]string)
	if headersStr == "" {
		return headers
	}
	for _, pair := range strings.Split(headersStr, ",") {
		if kv := strings.SplitN(pair, "=", 2); len(kv) == 2 {
			headers[strings.TrimSpace(kv[0])] = strings.TrimSpace(kv[1])
		}
	}
	return headers
}

// resolveProtocol resolves the OTLP protocol for a specific signal.
// It checks the signal-specific env var first, then the global OTEL_EXPORTER_OTLP_PROTOCOL,
// and defaults to "grpc".
func resolveProtocol(signalEnv string) string {
	protocol := os.Getenv(signalEnv)
	if protocol == "" {
		protocol = os.Getenv("OTEL_EXPORTER_OTLP_PROTOCOL")
	}
	if protocol == "" {
		protocol = "grpc"
	}
	return protocol
}

// Init initializes OpenTelemetry with the given configuration.
func Init(ctx context.Context, cfg Config) (*Telemetry, error) {
	t := &Telemetry{
		shutdown: make([]func(context.Context) error, 0),
	}

	// Set up propagator
	prop := propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	)
	otel.SetTextMapPropagator(prop)

	// Create resource
	resource := sdkresource.NewSchemaless(
		attribute.String("service.name", cfg.ServiceName),
		attribute.String("service.version", cfg.ServiceVersion),
	)

	// Initialize logger
	t.Logger = initLogger(cfg)

	if cfg.Endpoint == "" {
		t.Logger.Info("OTEL endpoint not configured, telemetry disabled")
		return t, nil
	}

	// Initialize tracer
	if err := t.initTracer(ctx, cfg, resource); err != nil {
		return nil, err
	}

	// Initialize metrics
	if cfg.MetricsEnabled {
		if err := t.initMetrics(ctx, cfg, resource); err != nil {
			t.Logger.Warn("failed to initialize metrics", "error", err)
		}
	}

	// Initialize logs export
	if cfg.LogsEnabled {
		if err := t.initLogs(ctx, cfg, resource); err != nil {
			t.Logger.Warn("failed to initialize OTEL logs", "error", err)
		} else {
			// Wrap the logger with OTEL handler
			logLevel := ParseLogLevel(os.Getenv("DAPR_MCP_SERVER_LOG_LEVEL"))
			jsonHandler := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: logLevel})
			otelHandler := NewOTELHandler(t.LoggerProvider, jsonHandler)
			t.Logger = slog.New(otelHandler).With(
				"service", cfg.ServiceName,
				"version", cfg.ServiceVersion,
			)
			slog.SetDefault(t.Logger)
		}
	}

	return t, nil
}

// initTracer initializes the trace provider.
func (t *Telemetry) initTracer(ctx context.Context, cfg Config, resource *sdkresource.Resource) error {
	var exporter sdktrace.SpanExporter
	var err error

	switch cfg.Protocol {
	case "grpc":
		cleanEndpoint := strings.TrimPrefix(cfg.Endpoint, "http://")
		cleanEndpoint = strings.TrimPrefix(cleanEndpoint, "https://")
		exporter, err = otlptracegrpc.New(ctx,
			otlptracegrpc.WithEndpoint(cleanEndpoint),
			otlptracegrpc.WithHeaders(cfg.Headers),
			otlptracegrpc.WithInsecure(),
		)
	case "http/protobuf", "http/json":
		endpoint := cfg.Endpoint
		if !strings.HasPrefix(endpoint, "http://") && !strings.HasPrefix(endpoint, "https://") {
			endpoint = "http://" + endpoint
		}
		exporter, err = otlptracehttp.New(ctx,
			otlptracehttp.WithEndpoint(endpoint),
			otlptracehttp.WithHeaders(cfg.Headers),
		)
	default:
		t.Logger.Warn("unsupported OTEL protocol, defaulting to grpc", "protocol", cfg.Protocol)
		cleanEndpoint := strings.TrimPrefix(cfg.Endpoint, "http://")
		cleanEndpoint = strings.TrimPrefix(cleanEndpoint, "https://")
		exporter, err = otlptracegrpc.New(ctx,
			otlptracegrpc.WithEndpoint(cleanEndpoint),
			otlptracegrpc.WithHeaders(cfg.Headers),
			otlptracegrpc.WithInsecure(),
		)
	}

	if err != nil {
		return err
	}

	t.TracerProvider = sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(resource),
	)
	otel.SetTracerProvider(t.TracerProvider)

	t.shutdown = append(t.shutdown, t.TracerProvider.Shutdown)
	t.Logger.Info("tracer initialized", "endpoint", cfg.Endpoint, "protocol", cfg.Protocol)

	return nil
}

// initMetrics initializes the metrics provider.
func (t *Telemetry) initMetrics(ctx context.Context, cfg Config, resource *sdkresource.Resource) error {
	metricsEndpoint := os.Getenv("OTEL_EXPORTER_OTLP_METRICS_ENDPOINT")
	if metricsEndpoint == "" {
		metricsEndpoint = cfg.Endpoint
	}

	protocol := resolveProtocol("OTEL_EXPORTER_OTLP_METRICS_PROTOCOL")

	var exporter sdkmetric.Exporter
	var err error

	switch protocol {
	case "http/protobuf", "http/json":
		endpoint := metricsEndpoint
		if !strings.HasPrefix(endpoint, "http://") && !strings.HasPrefix(endpoint, "https://") {
			endpoint = "http://" + endpoint
		}
		t.Logger.Debug("creating metrics exporter", "endpoint", endpoint, "protocol", protocol)
		exporter, err = otlpmetrichttp.New(ctx,
			otlpmetrichttp.WithEndpoint(endpoint),
			otlpmetrichttp.WithHeaders(cfg.Headers),
		)
	default:
		cleanEndpoint := strings.TrimPrefix(metricsEndpoint, "http://")
		cleanEndpoint = strings.TrimPrefix(cleanEndpoint, "https://")
		t.Logger.Debug("creating metrics exporter", "endpoint", cleanEndpoint, "protocol", protocol)
		exporter, err = otlpmetricgrpc.New(ctx,
			otlpmetricgrpc.WithEndpoint(cleanEndpoint),
			otlpmetricgrpc.WithHeaders(cfg.Headers),
			otlpmetricgrpc.WithInsecure(),
		)
	}

	if err != nil {
		t.Logger.Error("failed to create metrics exporter", "error", err)
		return err
	}

	// Configure flush interval (default 10s, configurable via env)
	flushInterval := 10 * time.Second
	if intervalStr := os.Getenv("OTEL_METRIC_EXPORT_INTERVAL"); intervalStr != "" {
		if parsed, err := time.ParseDuration(intervalStr); err == nil {
			flushInterval = parsed
		}
	}

	t.MeterProvider = sdkmetric.NewMeterProvider(
		sdkmetric.WithResource(resource),
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(exporter,
			sdkmetric.WithInterval(flushInterval),
		)),
	)
	otel.SetMeterProvider(t.MeterProvider)

	t.shutdown = append(t.shutdown, t.MeterProvider.Shutdown)
	t.Logger.Info("metrics initialized", "endpoint", metricsEndpoint, "flush_interval", flushInterval)

	return nil
}

// initLogs initializes the OTEL log provider.
func (t *Telemetry) initLogs(ctx context.Context, cfg Config, resource *sdkresource.Resource) error {
	logsEndpoint := os.Getenv("OTEL_EXPORTER_OTLP_LOGS_ENDPOINT")
	if logsEndpoint == "" {
		logsEndpoint = cfg.Endpoint
	}

	protocol := resolveProtocol("OTEL_EXPORTER_OTLP_LOGS_PROTOCOL")

	var exporter sdklog.Exporter
	var err error

	switch protocol {
	case "http/protobuf", "http/json":
		endpoint := logsEndpoint
		if !strings.HasPrefix(endpoint, "http://") && !strings.HasPrefix(endpoint, "https://") {
			endpoint = "http://" + endpoint
		}
		t.Logger.Debug("creating logs exporter", "endpoint", endpoint, "protocol", protocol)
		exporter, err = otlploghttp.New(ctx,
			otlploghttp.WithEndpoint(endpoint),
			otlploghttp.WithHeaders(cfg.Headers),
		)
	default:
		cleanEndpoint := strings.TrimPrefix(logsEndpoint, "http://")
		cleanEndpoint = strings.TrimPrefix(cleanEndpoint, "https://")
		t.Logger.Debug("creating logs exporter", "endpoint", cleanEndpoint, "protocol", protocol)
		exporter, err = otlploggrpc.New(ctx,
			otlploggrpc.WithEndpoint(cleanEndpoint),
			otlploggrpc.WithHeaders(cfg.Headers),
			otlploggrpc.WithInsecure(),
		)
	}

	if err != nil {
		t.Logger.Error("failed to create logs exporter", "error", err)
		return err
	}

	// Configure flush interval (default 5s, configurable via env)
	flushInterval := 5 * time.Second
	if intervalStr := os.Getenv("OTEL_LOG_EXPORT_INTERVAL"); intervalStr != "" {
		if parsed, err := time.ParseDuration(intervalStr); err == nil {
			flushInterval = parsed
		}
	}

	t.LoggerProvider = sdklog.NewLoggerProvider(
		sdklog.WithResource(resource),
		sdklog.WithProcessor(sdklog.NewBatchProcessor(exporter,
			sdklog.WithExportInterval(flushInterval),
		)),
	)

	t.shutdown = append(t.shutdown, t.LoggerProvider.Shutdown)
	t.Logger.Info("logs exporter initialized", "endpoint", logsEndpoint, "flush_interval", flushInterval)

	return nil
}

// ParseLogLevel maps a case-insensitive level name to a slog.Level,
// defaulting to Info for empty or unrecognized names.
func ParseLogLevel(name string) slog.Level {
	switch strings.ToUpper(name) {
	case "DEBUG":
		return slog.LevelDebug
	case "WARN", "WARNING":
		return slog.LevelWarn
	case "ERROR":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// initLogger initializes the structured logger.
func initLogger(cfg Config) *slog.Logger {
	opts := &slog.HandlerOptions{
		Level: ParseLogLevel(os.Getenv("DAPR_MCP_SERVER_LOG_LEVEL")),
	}

	handler := slog.NewJSONHandler(os.Stdout, opts)
	return slog.New(handler).With(
		"service", cfg.ServiceName,
		"version", cfg.ServiceVersion,
	)
}

// Shutdown gracefully shuts down all telemetry components.
func (t *Telemetry) Shutdown(ctx context.Context) error {
	var lastErr error
	for _, fn := range t.shutdown {
		if err := fn(ctx); err != nil {
			lastErr = err
			t.Logger.Error("shutdown error", "error", err)
		}
	}
	return lastErr
}

// Initialize initializes OpenTelemetry with default configuration and returns a shutdown function.
func Initialize(ctx context.Context) (func(context.Context) error, error) {
	cfg := DefaultConfig()
	t, err := Init(ctx, cfg)
	if err != nil {
		return nil, err
	}
	return t.Shutdown, nil
}
