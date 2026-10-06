package telemetry

import (
	"context"
	"log/slog"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestDefaultConfig(t *testing.T) {
	// Clear environment variables
	envVars := []string{
		"OTEL_EXPORTER_OTLP_TRACES_PROTOCOL",
		"OTEL_EXPORTER_OTLP_PROTOCOL",
		"OTEL_EXPORTER_OTLP_TRACES_ENDPOINT",
		"OTEL_EXPORTER_OTLP_ENDPOINT",
		"OTEL_EXPORTER_OTLP_HEADERS",
		"OTEL_SERVICE_NAME",
		"OTEL_SERVICE_VERSION",
		"DAPR_MCP_SERVER_METRICS_ENABLED",
		"DAPR_MCP_SERVER_LOGS_OTEL_ENABLED",
	}
	for _, v := range envVars {
		os.Unsetenv(v)
	}

	cfg := DefaultConfig()

	assert.Equal(t, "dapr-mcp-server", cfg.ServiceName)
	assert.Equal(t, "v1.0.0", cfg.ServiceVersion)
	assert.Equal(t, "grpc", cfg.Protocol)
	assert.Empty(t, cfg.Endpoint)
	assert.True(t, cfg.MetricsEnabled)
	assert.True(t, cfg.LogsEnabled)
}

func TestDefaultConfigWithEnvVars(t *testing.T) {
	// Set environment variables
	os.Setenv("OTEL_SERVICE_NAME", "test-service")
	os.Setenv("OTEL_SERVICE_VERSION", "v2.0.0")
	os.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "localhost:4317")
	os.Setenv("OTEL_EXPORTER_OTLP_PROTOCOL", "http/protobuf")
	os.Setenv("OTEL_EXPORTER_OTLP_HEADERS", "key1=value1,key2=value2")
	os.Setenv("DAPR_MCP_SERVER_METRICS_ENABLED", "false")
	os.Setenv("DAPR_MCP_SERVER_LOGS_OTEL_ENABLED", "false")

	defer func() {
		os.Unsetenv("OTEL_SERVICE_NAME")
		os.Unsetenv("OTEL_SERVICE_VERSION")
		os.Unsetenv("OTEL_EXPORTER_OTLP_ENDPOINT")
		os.Unsetenv("OTEL_EXPORTER_OTLP_PROTOCOL")
		os.Unsetenv("OTEL_EXPORTER_OTLP_HEADERS")
		os.Unsetenv("DAPR_MCP_SERVER_METRICS_ENABLED")
		os.Unsetenv("DAPR_MCP_SERVER_LOGS_OTEL_ENABLED")
	}()

	cfg := DefaultConfig()

	assert.Equal(t, "test-service", cfg.ServiceName)
	assert.Equal(t, "v2.0.0", cfg.ServiceVersion)
	assert.Equal(t, "localhost:4317", cfg.Endpoint)
	assert.Equal(t, "http/protobuf", cfg.Protocol)
	assert.Equal(t, "value1", cfg.Headers["key1"])
	assert.Equal(t, "value2", cfg.Headers["key2"])
	assert.False(t, cfg.MetricsEnabled)
	assert.False(t, cfg.LogsEnabled)
}

func TestDefaultConfigTracesProtocolPriority(t *testing.T) {
	// Traces-specific protocol should take priority
	os.Setenv("OTEL_EXPORTER_OTLP_TRACES_PROTOCOL", "http/json")
	os.Setenv("OTEL_EXPORTER_OTLP_PROTOCOL", "grpc")

	defer func() {
		os.Unsetenv("OTEL_EXPORTER_OTLP_TRACES_PROTOCOL")
		os.Unsetenv("OTEL_EXPORTER_OTLP_PROTOCOL")
	}()

	cfg := DefaultConfig()

	assert.Equal(t, "http/json", cfg.Protocol)
}

func TestDefaultConfigTracesEndpointPriority(t *testing.T) {
	// Traces-specific endpoint should take priority
	os.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", "traces.example.com:4317")
	os.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "general.example.com:4317")

	defer func() {
		os.Unsetenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT")
		os.Unsetenv("OTEL_EXPORTER_OTLP_ENDPOINT")
	}()

	cfg := DefaultConfig()

	assert.Equal(t, "traces.example.com:4317", cfg.Endpoint)
}

func TestParseHeaders(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected map[string]string
	}{
		{
			name:     "empty string",
			input:    "",
			expected: map[string]string{},
		},
		{
			name:     "single header",
			input:    "key=value",
			expected: map[string]string{"key": "value"},
		},
		{
			name:     "multiple headers",
			input:    "key1=value1,key2=value2,key3=value3",
			expected: map[string]string{"key1": "value1", "key2": "value2", "key3": "value3"},
		},
		{
			name:     "headers with spaces",
			input:    " key1 = value1 , key2 = value2 ",
			expected: map[string]string{"key1": "value1", "key2": "value2"},
		},
		{
			name:     "header with equals in value",
			input:    "auth=token=abc123",
			expected: map[string]string{"auth": "token=abc123"},
		},
		{
			name:     "invalid header without equals",
			input:    "key1=value1,invalidheader,key2=value2",
			expected: map[string]string{"key1": "value1", "key2": "value2"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := parseHeaders(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestInitWithoutEndpoint(t *testing.T) {
	cfg := Config{
		ServiceName:    "test-service",
		ServiceVersion: "v1.0.0",
		Endpoint:       "", // No endpoint configured
	}

	telemetry, err := Init(context.Background(), cfg)

	assert.NoError(t, err)
	assert.NotNil(t, telemetry)
	assert.NotNil(t, telemetry.Logger)
	assert.Nil(t, telemetry.TracerProvider)
	assert.Nil(t, telemetry.MeterProvider)
}

func TestTelemetryShutdownEmpty(t *testing.T) {
	telemetry := &Telemetry{
		shutdown: []func(context.Context) error{},
	}

	err := telemetry.Shutdown(context.Background())
	assert.NoError(t, err)
}

func TestTelemetryShutdownWithError(t *testing.T) {
	cfg := Config{
		ServiceName:    "test-service",
		ServiceVersion: "v1.0.0",
		Endpoint:       "",
	}

	telemetry, err := Init(context.Background(), cfg)
	assert.NoError(t, err)

	// Shutdown should not error even with no providers
	err = telemetry.Shutdown(context.Background())
	assert.NoError(t, err)
}

func TestInitLogger(t *testing.T) {
	tests := []struct {
		name     string
		logLevel string
	}{
		{"debug level", "DEBUG"},
		{"info level", "INFO"},
		{"warn level", "WARN"},
		{"warning level", "WARNING"},
		{"error level", "ERROR"},
		{"default level", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			os.Setenv("DAPR_MCP_SERVER_LOG_LEVEL", tt.logLevel)
			defer os.Unsetenv("DAPR_MCP_SERVER_LOG_LEVEL")

			cfg := Config{
				ServiceName:    "test",
				ServiceVersion: "v1",
			}

			logger := initLogger(cfg)
			assert.NotNil(t, logger)
		})
	}
}

func TestConfigStruct(t *testing.T) {
	cfg := Config{
		ServiceName:    "test-service",
		ServiceVersion: "v1.0.0",
		Endpoint:       "localhost:4317",
		Protocol:       "grpc",
		Headers:        map[string]string{"key": "value"},
		MetricsEnabled: true,
		LogsEnabled:    true,
	}

	assert.Equal(t, "test-service", cfg.ServiceName)
	assert.Equal(t, "v1.0.0", cfg.ServiceVersion)
	assert.Equal(t, "localhost:4317", cfg.Endpoint)
	assert.Equal(t, "grpc", cfg.Protocol)
	assert.Equal(t, "value", cfg.Headers["key"])
	assert.True(t, cfg.MetricsEnabled)
	assert.True(t, cfg.LogsEnabled)
}

func TestTelemetryStruct(t *testing.T) {
	telemetry := &Telemetry{
		TracerProvider: nil,
		MeterProvider:  nil,
		Logger:         nil,
		shutdown:       make([]func(context.Context) error, 0),
	}

	assert.Nil(t, telemetry.TracerProvider)
	assert.Nil(t, telemetry.MeterProvider)
	assert.Nil(t, telemetry.Logger)
	assert.Empty(t, telemetry.shutdown)
}

func TestInitialize(t *testing.T) {
	// Clear environment to ensure no endpoint is configured
	os.Unsetenv("OTEL_EXPORTER_OTLP_ENDPOINT")
	os.Unsetenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT")

	shutdown, err := Initialize(context.Background())

	assert.NoError(t, err)
	assert.NotNil(t, shutdown)

	// Call shutdown to clean up
	err = shutdown(context.Background())
	assert.NoError(t, err)
}

func TestResolveProtocol(t *testing.T) {
	tests := []struct {
		name      string
		signalEnv string
		signalVal string
		globalVal string
		expected  string
	}{
		{
			name:      "signal-specific override",
			signalEnv: "OTEL_EXPORTER_OTLP_METRICS_PROTOCOL",
			signalVal: "http/protobuf",
			globalVal: "grpc",
			expected:  "http/protobuf",
		},
		{
			name:      "falls back to global",
			signalEnv: "OTEL_EXPORTER_OTLP_METRICS_PROTOCOL",
			signalVal: "",
			globalVal: "http/protobuf",
			expected:  "http/protobuf",
		},
		{
			name:      "defaults to grpc",
			signalEnv: "OTEL_EXPORTER_OTLP_METRICS_PROTOCOL",
			signalVal: "",
			globalVal: "",
			expected:  "grpc",
		},
		{
			name:      "logs signal override",
			signalEnv: "OTEL_EXPORTER_OTLP_LOGS_PROTOCOL",
			signalVal: "http/json",
			globalVal: "grpc",
			expected:  "http/json",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			os.Unsetenv(tt.signalEnv)
			os.Unsetenv("OTEL_EXPORTER_OTLP_PROTOCOL")

			if tt.signalVal != "" {
				os.Setenv(tt.signalEnv, tt.signalVal)
			}
			if tt.globalVal != "" {
				os.Setenv("OTEL_EXPORTER_OTLP_PROTOCOL", tt.globalVal)
			}

			defer func() {
				os.Unsetenv(tt.signalEnv)
				os.Unsetenv("OTEL_EXPORTER_OTLP_PROTOCOL")
			}()

			result := resolveProtocol(tt.signalEnv)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestParseLogLevel(t *testing.T) {
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
			assert.Equal(t, tt.want, ParseLogLevel(tt.input))
		})
	}
}
