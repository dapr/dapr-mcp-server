package telemetry

import (
	"bytes"
	"context"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
)

// otelEnvVars lists every variable Init or the exporters read, so tests can blank them.
var otelEnvVars = []string{
	envOTLPEndpoint, envOTLPProtocol, envOTLPInsecure, envOTLPHeaders,
	"OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", "OTEL_EXPORTER_OTLP_TRACES_PROTOCOL",
	"OTEL_EXPORTER_OTLP_TRACES_INSECURE", "OTEL_EXPORTER_OTLP_TRACES_HEADERS",
	"OTEL_EXPORTER_OTLP_METRICS_ENDPOINT", "OTEL_EXPORTER_OTLP_METRICS_PROTOCOL",
	"OTEL_EXPORTER_OTLP_METRICS_INSECURE", "OTEL_EXPORTER_OTLP_METRICS_HEADERS",
	"OTEL_EXPORTER_OTLP_LOGS_ENDPOINT", "OTEL_EXPORTER_OTLP_LOGS_PROTOCOL",
	"OTEL_EXPORTER_OTLP_LOGS_INSECURE", "OTEL_EXPORTER_OTLP_LOGS_HEADERS",
	envServiceName, envServiceVersion, envMetricsEnabled, envLogsEnabled,
	envMetricExportPeriod, envLogExportPeriod, envLogLevel,
}

// clearOTELEnv blanks the OTEL environment and restores the default slog logger afterwards.
func clearOTELEnv(t *testing.T) {
	t.Helper()
	for _, v := range otelEnvVars {
		t.Setenv(v, "")
	}
	prev := slog.Default()
	t.Cleanup(func() { slog.SetDefault(prev) })
}

func TestDefaultConfig(t *testing.T) {
	clearOTELEnv(t)

	cfg := DefaultConfig()

	assert.Equal(t, "dapr-mcp-server", cfg.ServiceName)
	assert.Equal(t, "v1.0.0", cfg.ServiceVersion)
	assert.Equal(t, ProtocolGRPC, cfg.Protocol)
	assert.Empty(t, cfg.Endpoint)
	assert.False(t, cfg.Insecure)
	assert.True(t, cfg.MetricsEnabled)
	assert.True(t, cfg.LogsEnabled)
}

func TestDefaultConfigWithEnvVars(t *testing.T) {
	clearOTELEnv(t)
	t.Setenv(envServiceName, "test-service")
	t.Setenv(envServiceVersion, "v2.0.0")
	t.Setenv(envOTLPEndpoint, "localhost:4317")
	t.Setenv(envOTLPProtocol, ProtocolHTTPProtobuf)
	t.Setenv(envOTLPHeaders, "key1=value1,key2=value2")
	t.Setenv(envOTLPInsecure, "true")
	t.Setenv(envMetricsEnabled, "false")
	t.Setenv(envLogsEnabled, "false")

	cfg := DefaultConfig()

	assert.Equal(t, "test-service", cfg.ServiceName)
	assert.Equal(t, "v2.0.0", cfg.ServiceVersion)
	assert.Equal(t, "localhost:4317", cfg.Endpoint)
	assert.Equal(t, ProtocolHTTPProtobuf, cfg.Protocol)
	assert.Equal(t, map[string]string{"key1": "value1", "key2": "value2"}, cfg.Headers)
	assert.True(t, cfg.Insecure)
	assert.False(t, cfg.MetricsEnabled)
	assert.False(t, cfg.LogsEnabled)
}

func TestDefaultConfigIgnoresSignalSpecificEnv(t *testing.T) {
	clearOTELEnv(t)
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", "traces.example.com:4317")
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_PROTOCOL", ProtocolHTTPProtobuf)
	t.Setenv(envOTLPEndpoint, "general.example.com:4317")

	cfg := DefaultConfig()

	assert.Equal(t, "general.example.com:4317", cfg.Endpoint)
	assert.Equal(t, ProtocolGRPC, cfg.Protocol)
}

func TestInitWithoutEndpoint(t *testing.T) {
	clearOTELEnv(t)

	tel, err := Init(context.Background(), Config{ServiceName: "test-service", ServiceVersion: "v1.0.0"})

	require.NoError(t, err)
	require.NotNil(t, tel)
	assert.NotNil(t, tel.Logger)
	assert.Nil(t, tel.TracerProvider)
	assert.Nil(t, tel.MeterProvider)
	assert.Nil(t, tel.LoggerProvider)
	assert.NoError(t, tel.Shutdown(context.Background()))
}

func TestInitInvalidTraceEndpoint(t *testing.T) {
	clearOTELEnv(t)

	_, err := Init(context.Background(), Config{Endpoint: "ftp://collector:4317"})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "init tracer")
}

// otlpHTTPRecorder is a fake OTLP/HTTP collector that counts requests per path.
type otlpHTTPRecorder struct {
	mu    sync.Mutex
	paths map[string]int
}

func newOTLPHTTPServer(t *testing.T) (*httptest.Server, *otlpHTTPRecorder) {
	t.Helper()
	rec := &otlpHTTPRecorder{paths: map[string]int{}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.mu.Lock()
		rec.paths[r.URL.Path]++
		rec.mu.Unlock()
		w.Header().Set("Content-Type", "application/x-protobuf")
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	return srv, rec
}

func (r *otlpHTTPRecorder) count(path string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.paths[path]
}

// emitAllSignals produces one span, one metric point and one log record.
func emitAllSignals(t *testing.T, tel *Telemetry) {
	t.Helper()
	ctx := context.Background()

	_, span := tel.TracerProvider.Tracer("test").Start(ctx, "op")
	span.End()

	counter, err := tel.MeterProvider.Meter("test").Int64Counter("test.counter")
	require.NoError(t, err)
	counter.Add(ctx, 1)

	tel.Logger.Info("hello from test")
}

func TestInitExportsEverySignalOverHTTP(t *testing.T) {
	clearOTELEnv(t)
	srv, rec := newOTLPHTTPServer(t)

	tel, err := Init(context.Background(), Config{
		ServiceName:    "svc",
		ServiceVersion: "v1",
		Endpoint:       srv.URL + "/base",
		Protocol:       ProtocolHTTPProtobuf,
		MetricsEnabled: true,
		LogsEnabled:    true,
	})
	require.NoError(t, err)
	require.NotNil(t, tel.TracerProvider)
	require.NotNil(t, tel.MeterProvider)
	require.NotNil(t, tel.LoggerProvider)

	emitAllSignals(t, tel)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.NoError(t, tel.Shutdown(ctx))

	assert.Positive(t, rec.count("/base/v1/traces"))
	assert.Positive(t, rec.count("/base/v1/metrics"))
	assert.Positive(t, rec.count("/base/v1/logs"))
}

func TestInitSignalSpecificEndpointOverHTTP(t *testing.T) {
	clearOTELEnv(t)
	srv, rec := newOTLPHTTPServer(t)
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", srv.URL+"/custom/traces")
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_PROTOCOL", ProtocolHTTPProtobuf)

	tel, err := Init(context.Background(), Config{MetricsEnabled: true, LogsEnabled: true})
	require.NoError(t, err)
	require.NotNil(t, tel.TracerProvider)
	assert.Nil(t, tel.MeterProvider, "metrics have no endpoint")
	assert.Nil(t, tel.LoggerProvider, "logs have no endpoint")

	_, span := tel.TracerProvider.Tracer("test").Start(context.Background(), "op")
	span.End()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.NoError(t, tel.Shutdown(ctx))
	assert.Positive(t, rec.count("/custom/traces"))
}

func TestInitInvalidMetricsAndLogsEndpointsOnlyWarn(t *testing.T) {
	clearOTELEnv(t)
	t.Setenv("OTEL_EXPORTER_OTLP_METRICS_ENDPOINT", "ftp://bad")
	t.Setenv("OTEL_EXPORTER_OTLP_LOGS_ENDPOINT", "ftp://bad")

	tel, err := Init(context.Background(), Config{MetricsEnabled: true, LogsEnabled: true})
	require.NoError(t, err)
	assert.Nil(t, tel.MeterProvider)
	assert.Nil(t, tel.LoggerProvider)
}

// grpcMethodRecorder captures the full method name of every RPC a gRPC server receives.
type grpcMethodRecorder struct {
	mu      sync.Mutex
	methods map[string]int
}

func (r *grpcMethodRecorder) count(method string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.methods[method]
}

func newOTLPGRPCServer(t *testing.T) (string, *grpcMethodRecorder) {
	t.Helper()
	rec := &grpcMethodRecorder{methods: map[string]int{}}

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	srv := grpc.NewServer(grpc.UnknownServiceHandler(func(_ any, stream grpc.ServerStream) error {
		method, _ := grpc.MethodFromServerStream(stream)
		rec.mu.Lock()
		rec.methods[method]++
		rec.mu.Unlock()
		return nil
	}))
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)

	return "http://" + lis.Addr().String(), rec
}

func TestInitExportsEverySignalOverGRPC(t *testing.T) {
	clearOTELEnv(t)
	endpoint, rec := newOTLPGRPCServer(t)

	tel, err := Init(context.Background(), Config{
		Endpoint:       endpoint,
		Protocol:       ProtocolGRPC,
		MetricsEnabled: true,
		LogsEnabled:    true,
	})
	require.NoError(t, err)

	emitAllSignals(t, tel)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// The fake server answers without a response message, so exports may report errors;
	// the point is that each exporter reached it over plaintext gRPC.
	_ = tel.Shutdown(ctx)

	assert.Positive(t, rec.count("/opentelemetry.proto.collector.trace.v1.TraceService/Export"))
	assert.Positive(t, rec.count("/opentelemetry.proto.collector.metrics.v1.MetricsService/Export"))
	assert.Positive(t, rec.count("/opentelemetry.proto.collector.logs.v1.LogsService/Export"))
}

func TestInitUnknownProtocolFallsBackToGRPC(t *testing.T) {
	clearOTELEnv(t)
	endpoint, rec := newOTLPGRPCServer(t)

	tel, err := Init(context.Background(), Config{Endpoint: endpoint, Protocol: "thrift"})
	require.NoError(t, err)

	_, span := tel.TracerProvider.Tracer("test").Start(context.Background(), "op")
	span.End()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = tel.Shutdown(ctx)

	assert.Positive(t, rec.count("/opentelemetry.proto.collector.trace.v1.TraceService/Export"))
}

func TestTelemetryShutdownEmpty(t *testing.T) {
	t.Parallel()

	tel := &Telemetry{Logger: slog.New(slog.DiscardHandler)}
	assert.NoError(t, tel.Shutdown(context.Background()))
}

func TestNewBaseHandler(t *testing.T) {
	tests := []struct {
		name       string
		logLevel   string
		wantDebug  bool
		wantInfo   bool
		wantErrors bool
	}{
		{name: "debug level", logLevel: "DEBUG", wantDebug: true, wantInfo: true, wantErrors: true},
		{name: "info level", logLevel: "INFO", wantInfo: true, wantErrors: true},
		{name: "error level", logLevel: "ERROR", wantErrors: true},
		{name: "default level", logLevel: "", wantInfo: true, wantErrors: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(envLogLevel, tt.logLevel)

			var buf bytes.Buffer
			logger := withServiceAttrs(slog.New(newBaseHandler(&buf)), Config{ServiceName: "svc", ServiceVersion: "v1"})
			logger.Debug("debug-msg")
			logger.Info("info-msg")
			logger.Error("error-msg")

			out := buf.String()
			assert.Equal(t, tt.wantDebug, strings.Contains(out, "debug-msg"))
			assert.Equal(t, tt.wantInfo, strings.Contains(out, "info-msg"))
			assert.Equal(t, tt.wantErrors, strings.Contains(out, "error-msg"))
			assert.Contains(t, out, `"service":"svc"`)
			assert.Contains(t, out, `"version":"v1"`)
		})
	}
}

func TestInitialize(t *testing.T) {
	clearOTELEnv(t)

	shutdown, err := Initialize(context.Background())

	require.NoError(t, err)
	require.NotNil(t, shutdown)
	assert.NoError(t, shutdown(context.Background()))
}

func TestParseLogLevel(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
		want  slog.Level
	}{
		{name: "empty defaults to info", input: "", want: slog.LevelInfo},
		{name: "unknown defaults to info", input: "verbose", want: slog.LevelInfo},
		{name: "debug", input: "DEBUG", want: slog.LevelDebug},
		{name: "case insensitive", input: "debug", want: slog.LevelDebug},
		{name: "warn", input: "WARN", want: slog.LevelWarn},
		{name: "warning alias", input: "warning", want: slog.LevelWarn},
		{name: "error", input: "ERROR", want: slog.LevelError},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, ParseLogLevel(tt.input))
		})
	}
}
