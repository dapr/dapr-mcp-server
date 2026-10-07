package telemetry

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strconv"
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

const (
	envLogLevel           = "DAPR_MCP_SERVER_LOG_LEVEL"
	envMetricsEnabled     = "DAPR_MCP_SERVER_METRICS_ENABLED"
	envLogsEnabled        = "DAPR_MCP_SERVER_LOGS_OTEL_ENABLED"
	envServiceName        = "OTEL_SERVICE_NAME"
	envServiceVersion     = "OTEL_SERVICE_VERSION"
	envMetricExportPeriod = "OTEL_METRIC_EXPORT_INTERVAL"
	envLogExportPeriod    = "OTEL_LOG_EXPORT_INTERVAL"

	// DefaultServiceName is the service.name reported when OTEL_SERVICE_NAME is unset.
	DefaultServiceName = "dapr-mcp-server"

	// DefaultServiceVersion is the service.version reported when neither
	// OTEL_SERVICE_VERSION nor WithServiceVersion supplies one.
	DefaultServiceVersion = "unknown"

	// instrumentationName names the tracer, meter and logger this package creates.
	instrumentationName = "dapr-mcp-server"

	defaultMetricExportInterval = 10 * time.Second
	defaultLogExportInterval    = 5 * time.Second
)

// Config holds the telemetry configuration.
//
// Endpoint, Protocol, Insecure and Headers are the generic OTLP settings.
// The signal-specific OTEL_EXPORTER_OTLP_{TRACES,METRICS,LOGS}_* variables
// override them per signal when Init runs.
type Config struct {
	ServiceName    string
	ServiceVersion string
	Endpoint       string
	Protocol       string // ProtocolGRPC or ProtocolHTTPProtobuf
	Headers        map[string]string
	MetricsEnabled bool
	LogsEnabled    bool
	// Insecure selects plaintext for an Endpoint given without a scheme.
	// An explicit http:// or https:// scheme always wins.
	Insecure bool
}

// Telemetry holds the telemetry providers.
type Telemetry struct {
	TracerProvider *sdktrace.TracerProvider
	MeterProvider  *sdkmetric.MeterProvider
	LoggerProvider *sdklog.LoggerProvider
	Logger         *slog.Logger
	shutdown       []shutdownStep
}

// shutdownStep is one provider's shutdown, named for error messages.
type shutdownStep struct {
	name string
	fn   func(context.Context) error
}

// DefaultConfig returns a configuration from the generic OTEL_* environment variables.
func DefaultConfig() Config {
	serviceName := os.Getenv(envServiceName)
	if serviceName == "" {
		serviceName = DefaultServiceName
	}

	serviceVersion := os.Getenv(envServiceVersion)
	if serviceVersion == "" {
		serviceVersion = DefaultServiceVersion
	}

	insecure := false
	if raw := os.Getenv(envOTLPInsecure); raw != "" {
		insecure = parseBoolEnv(envOTLPInsecure, raw, false, slog.Default())
	}

	return Config{
		ServiceName:    serviceName,
		ServiceVersion: serviceVersion,
		Endpoint:       os.Getenv(envOTLPEndpoint),
		Protocol:       firstNonEmpty(os.Getenv(envOTLPProtocol), ProtocolGRPC),
		Headers:        parseHeaders(os.Getenv(envOTLPHeaders)),
		MetricsEnabled: os.Getenv(envMetricsEnabled) != "false",
		LogsEnabled:    os.Getenv(envLogsEnabled) != "false",
		Insecure:       insecure,
	}
}

// Init initializes OpenTelemetry with the given configuration.
// Each signal is exported only when it has an endpoint,
// from either cfg.Endpoint or its signal-specific variable.
func Init(ctx context.Context, cfg Config) (*Telemetry, error) {
	t := &Telemetry{}

	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))

	resource := sdkresource.NewSchemaless(
		attribute.String("service.name", cfg.ServiceName),
		attribute.String("service.version", cfg.ServiceVersion),
	)

	baseHandler := newBaseHandler(os.Stderr)
	t.Logger = withServiceAttrs(slog.New(baseHandler), cfg)
	slog.SetDefault(t.Logger)

	if err := t.initTracer(ctx, cfg, resource); err != nil {
		return nil, fmt.Errorf("init tracer: %w", err)
	}

	if cfg.MetricsEnabled {
		if err := t.initMetrics(ctx, cfg, resource); err != nil {
			t.Logger.Warn("failed to initialize metrics", "error", fmt.Errorf("init metrics: %w", err))
		}
	}

	if cfg.LogsEnabled {
		if err := t.initLogs(ctx, cfg, resource); err != nil {
			t.Logger.Warn("failed to initialize OTEL logs", "error", fmt.Errorf("init logs: %w", err))
		} else if t.LoggerProvider != nil {
			t.Logger = withServiceAttrs(slog.New(NewOTELHandler(t.LoggerProvider, baseHandler)), cfg)
			slog.SetDefault(t.Logger)
		}
	}

	if t.TracerProvider == nil && t.MeterProvider == nil && t.LoggerProvider == nil {
		t.Logger.Info("OTEL endpoint not configured, telemetry disabled")
	}
	return t, nil
}

// initTracer initializes the trace provider, or does nothing when traces have no endpoint.
func (t *Telemetry) initTracer(ctx context.Context, cfg Config, resource *sdkresource.Resource) error {
	target, ok, err := resolveExporterTarget(cfg, signalTraces, os.Getenv, t.Logger)
	if err != nil || !ok {
		return err
	}

	var exporter sdktrace.SpanExporter
	if target.Protocol == ProtocolHTTPProtobuf {
		exporter, err = otlptracehttp.New(ctx,
			otlptracehttp.WithEndpointURL(target.URL),
			otlptracehttp.WithHeaders(target.Headers),
		)
	} else {
		exporter, err = otlptracegrpc.New(ctx,
			otlptracegrpc.WithEndpointURL(target.URL),
			otlptracegrpc.WithHeaders(target.Headers),
		)
	}
	if err != nil {
		return fmt.Errorf("create trace exporter: %w", err)
	}

	t.TracerProvider = sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(resource),
	)
	otel.SetTracerProvider(t.TracerProvider)

	t.shutdown = append(t.shutdown, shutdownStep{name: signalTraces.name, fn: t.TracerProvider.Shutdown})
	t.Logger.Info("tracer initialized", "endpoint", target.URL, "protocol", target.Protocol)
	return nil
}

// initMetrics initializes the metrics provider, or does nothing when metrics have no endpoint.
func (t *Telemetry) initMetrics(ctx context.Context, cfg Config, resource *sdkresource.Resource) error {
	target, ok, err := resolveExporterTarget(cfg, signalMetrics, os.Getenv, t.Logger)
	if err != nil || !ok {
		return err
	}

	var exporter sdkmetric.Exporter
	if target.Protocol == ProtocolHTTPProtobuf {
		exporter, err = otlpmetrichttp.New(ctx,
			otlpmetrichttp.WithEndpointURL(target.URL),
			otlpmetrichttp.WithHeaders(target.Headers),
		)
	} else {
		exporter, err = otlpmetricgrpc.New(ctx,
			otlpmetricgrpc.WithEndpointURL(target.URL),
			otlpmetricgrpc.WithHeaders(target.Headers),
		)
	}
	if err != nil {
		return fmt.Errorf("create metric exporter: %w", err)
	}

	flushInterval := parseExportInterval(envMetricExportPeriod, os.Getenv(envMetricExportPeriod), defaultMetricExportInterval, t.Logger)

	t.MeterProvider = sdkmetric.NewMeterProvider(
		sdkmetric.WithResource(resource),
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(exporter,
			sdkmetric.WithInterval(flushInterval),
		)),
	)
	otel.SetMeterProvider(t.MeterProvider)

	t.shutdown = append(t.shutdown, shutdownStep{name: signalMetrics.name, fn: t.MeterProvider.Shutdown})
	t.Logger.Info("metrics initialized", "endpoint", target.URL, "protocol", target.Protocol, "flush_interval", flushInterval)
	return nil
}

// initLogs initializes the OTEL log provider, or does nothing when logs have no endpoint.
func (t *Telemetry) initLogs(ctx context.Context, cfg Config, resource *sdkresource.Resource) error {
	target, ok, err := resolveExporterTarget(cfg, signalLogs, os.Getenv, t.Logger)
	if err != nil || !ok {
		return err
	}

	var exporter sdklog.Exporter
	if target.Protocol == ProtocolHTTPProtobuf {
		exporter, err = otlploghttp.New(ctx,
			otlploghttp.WithEndpointURL(target.URL),
			otlploghttp.WithHeaders(target.Headers),
		)
	} else {
		exporter, err = otlploggrpc.New(ctx,
			otlploggrpc.WithEndpointURL(target.URL),
			otlploggrpc.WithHeaders(target.Headers),
		)
	}
	if err != nil {
		return fmt.Errorf("create log exporter: %w", err)
	}

	flushInterval := parseExportInterval(envLogExportPeriod, os.Getenv(envLogExportPeriod), defaultLogExportInterval, t.Logger)

	t.LoggerProvider = sdklog.NewLoggerProvider(
		sdklog.WithResource(resource),
		sdklog.WithProcessor(sdklog.NewBatchProcessor(exporter,
			sdklog.WithExportInterval(flushInterval),
		)),
	)

	t.shutdown = append(t.shutdown, shutdownStep{name: signalLogs.name, fn: t.LoggerProvider.Shutdown})
	t.Logger.Info("logs exporter initialized", "endpoint", target.URL, "protocol", target.Protocol, "flush_interval", flushInterval)
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

// newBaseHandler returns the JSON handler every logger writes through.
// Callers pass os.Stderr: stdout carries the MCP stdio transport,
// so anything logged there would corrupt the protocol stream.
func newBaseHandler(w io.Writer) slog.Handler {
	return slog.NewJSONHandler(w, &slog.HandlerOptions{
		Level: ParseLogLevel(os.Getenv(envLogLevel)),
	})
}

// withServiceAttrs tags every record from logger with the service identity.
func withServiceAttrs(logger *slog.Logger, cfg Config) *slog.Logger {
	return logger.With("service", cfg.ServiceName, "version", cfg.ServiceVersion)
}

// Shutdown flushes and stops every provider and returns all of their errors joined.
//
// Providers stop in the order they were started: traces, metrics, then logs.
// Logs go last so that errors logged while the others flush are still exported;
// once the log provider is down, records still reach stderr through the base handler.
func (t *Telemetry) Shutdown(ctx context.Context) error {
	var errs []error
	for _, step := range t.shutdown {
		if err := step.fn(ctx); err != nil {
			err = fmt.Errorf("shutdown %s: %w", step.name, err)
			errs = append(errs, err)
			t.Logger.Error("telemetry shutdown failed", "error", err)
		}
	}
	return errors.Join(errs...)
}

// parseExportInterval reads an export interval variable.
// Bare integers are milliseconds, as the OTEL spec defines them;
// Go durations such as "30s" are accepted for backward compatibility.
// Empty, invalid or non-positive values log a warning and return def.
func parseExportInterval(name, raw string, def time.Duration, logger *slog.Logger) time.Duration {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return def
	}
	if ms, err := strconv.ParseInt(raw, 10, 64); err == nil {
		if ms > 0 {
			return time.Duration(ms) * time.Millisecond
		}
	} else if d, err := time.ParseDuration(raw); err == nil && d > 0 {
		return d
	}
	logger.Warn("ignoring invalid export interval", "name", name, "value", raw, "default", def)
	return def
}

// Option customizes Initialize.
type Option func(*options)

type options struct {
	serviceVersion string
}

// WithServiceVersion sets the service.version reported when OTEL_SERVICE_VERSION is unset,
// typically the version stamped into the binary at build time.
func WithServiceVersion(version string) Option {
	return func(o *options) { o.serviceVersion = version }
}

// configFromOptions returns DefaultConfig with opts applied.
func configFromOptions(opts []Option) Config {
	var o options
	for _, opt := range opts {
		opt(&o)
	}

	cfg := DefaultConfig()
	if os.Getenv(envServiceVersion) == "" && o.serviceVersion != "" {
		cfg.ServiceVersion = o.serviceVersion
	}
	return cfg
}

// Initialize initializes OpenTelemetry with DefaultConfig and returns a shutdown function.
func Initialize(ctx context.Context, opts ...Option) (func(context.Context) error, error) {
	t, err := Init(ctx, configFromOptions(opts))
	if err != nil {
		return nil, err
	}
	return t.Shutdown, nil
}
