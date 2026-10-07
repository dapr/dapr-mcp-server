package telemetry

import (
	"bytes"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func envMap(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestResolveExporterTarget(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		cfg         Config
		sig         signal
		env         map[string]string
		wantOK      bool
		wantErr     bool
		wantURL     string
		wantProto   string
		wantHeaders map[string]string
	}{
		{
			name:   "no endpoint disables the signal",
			sig:    signalTraces,
			wantOK: false,
		},
		{
			name:      "http generic endpoint without path gets the signal path",
			cfg:       Config{Endpoint: "http://collector:4318", Protocol: ProtocolHTTPProtobuf},
			sig:       signalTraces,
			wantOK:    true,
			wantURL:   "http://collector:4318/v1/traces",
			wantProto: ProtocolHTTPProtobuf,
		},
		{
			name:      "http generic endpoint with trailing slash",
			cfg:       Config{Endpoint: "http://collector:4318/", Protocol: ProtocolHTTPProtobuf},
			sig:       signalMetrics,
			wantOK:    true,
			wantURL:   "http://collector:4318/v1/metrics",
			wantProto: ProtocolHTTPProtobuf,
		},
		{
			name:      "http generic endpoint with base path appends the signal path",
			cfg:       Config{Endpoint: "https://gw.example.com/otlp", Protocol: ProtocolHTTPProtobuf},
			sig:       signalLogs,
			wantOK:    true,
			wantURL:   "https://gw.example.com/otlp/v1/logs",
			wantProto: ProtocolHTTPProtobuf,
		},
		{
			name:      "signal-specific endpoint with path is used as-is",
			cfg:       Config{Endpoint: "http://generic:4318", Protocol: ProtocolHTTPProtobuf},
			sig:       signalTraces,
			env:       map[string]string{"OTEL_EXPORTER_OTLP_TRACES_ENDPOINT": "http://traces:4318/custom/path"},
			wantOK:    true,
			wantURL:   "http://traces:4318/custom/path",
			wantProto: ProtocolHTTPProtobuf,
		},
		{
			name:      "signal-specific endpoint without path gets the signal path",
			cfg:       Config{Protocol: ProtocolHTTPProtobuf},
			sig:       signalMetrics,
			env:       map[string]string{"OTEL_EXPORTER_OTLP_METRICS_ENDPOINT": "http://metrics:4318"},
			wantOK:    true,
			wantURL:   "http://metrics:4318/v1/metrics",
			wantProto: ProtocolHTTPProtobuf,
		},
		{
			name:      "signal-specific endpoint only affects its own signal",
			cfg:       Config{Endpoint: "http://generic:4318", Protocol: ProtocolHTTPProtobuf},
			sig:       signalLogs,
			env:       map[string]string{"OTEL_EXPORTER_OTLP_TRACES_ENDPOINT": "http://traces:4318/x"},
			wantOK:    true,
			wantURL:   "http://generic:4318/v1/logs",
			wantProto: ProtocolHTTPProtobuf,
		},
		{
			name:      "grpc keeps https and drops the path",
			cfg:       Config{Endpoint: "https://collector:4317/ignored", Protocol: ProtocolGRPC},
			sig:       signalTraces,
			wantOK:    true,
			wantURL:   "https://collector:4317",
			wantProto: ProtocolGRPC,
		},
		{
			name:      "grpc with http scheme is plaintext",
			cfg:       Config{Endpoint: "http://collector:4317", Protocol: ProtocolGRPC},
			sig:       signalTraces,
			wantOK:    true,
			wantURL:   "http://collector:4317",
			wantProto: ProtocolGRPC,
		},
		{
			name:      "scheme-less endpoint defaults to TLS",
			cfg:       Config{Endpoint: "collector:4317"},
			sig:       signalTraces,
			wantOK:    true,
			wantURL:   "https://collector:4317",
			wantProto: ProtocolGRPC,
		},
		{
			name:      "scheme-less endpoint with generic insecure is plaintext",
			cfg:       Config{Endpoint: "collector:4317", Insecure: true},
			sig:       signalTraces,
			wantOK:    true,
			wantURL:   "http://collector:4317",
			wantProto: ProtocolGRPC,
		},
		{
			name:      "signal-specific insecure overrides the generic one",
			cfg:       Config{Endpoint: "collector:4317", Insecure: true},
			sig:       signalMetrics,
			env:       map[string]string{"OTEL_EXPORTER_OTLP_METRICS_INSECURE": "false"},
			wantOK:    true,
			wantURL:   "https://collector:4317",
			wantProto: ProtocolGRPC,
		},
		{
			name:      "signal-specific insecure enables plaintext",
			cfg:       Config{Endpoint: "collector:4318", Protocol: ProtocolHTTPProtobuf},
			sig:       signalLogs,
			env:       map[string]string{"OTEL_EXPORTER_OTLP_LOGS_INSECURE": "true"},
			wantOK:    true,
			wantURL:   "http://collector:4318/v1/logs",
			wantProto: ProtocolHTTPProtobuf,
		},
		{
			name:      "invalid insecure value falls back to config",
			cfg:       Config{Endpoint: "collector:4317", Insecure: true},
			sig:       signalTraces,
			env:       map[string]string{"OTEL_EXPORTER_OTLP_TRACES_INSECURE": "maybe"},
			wantOK:    true,
			wantURL:   "http://collector:4317",
			wantProto: ProtocolGRPC,
		},
		{
			name:      "explicit https scheme ignores insecure",
			cfg:       Config{Endpoint: "https://collector:4317", Insecure: true},
			sig:       signalTraces,
			wantOK:    true,
			wantURL:   "https://collector:4317",
			wantProto: ProtocolGRPC,
		},
		{
			name:      "signal-specific protocol overrides config",
			cfg:       Config{Endpoint: "http://collector:4318", Protocol: ProtocolGRPC},
			sig:       signalMetrics,
			env:       map[string]string{"OTEL_EXPORTER_OTLP_METRICS_PROTOCOL": ProtocolHTTPProtobuf},
			wantOK:    true,
			wantURL:   "http://collector:4318/v1/metrics",
			wantProto: ProtocolHTTPProtobuf,
		},
		{
			name:      "http/json falls back to http/protobuf",
			cfg:       Config{Endpoint: "http://collector:4318", Protocol: ProtocolHTTPJSON},
			sig:       signalTraces,
			wantOK:    true,
			wantURL:   "http://collector:4318/v1/traces",
			wantProto: ProtocolHTTPProtobuf,
		},
		{
			name:      "unknown protocol falls back to grpc",
			cfg:       Config{Endpoint: "http://collector:4317", Protocol: "carrier-pigeon"},
			sig:       signalTraces,
			wantOK:    true,
			wantURL:   "http://collector:4317",
			wantProto: ProtocolGRPC,
		},
		{
			name: "signal headers merge over generic headers",
			cfg: Config{
				Endpoint: "http://collector:4317",
				Headers:  map[string]string{"a": "1", "b": "2"},
			},
			sig:         signalTraces,
			env:         map[string]string{"OTEL_EXPORTER_OTLP_TRACES_HEADERS": "b=3,c=4"},
			wantOK:      true,
			wantURL:     "http://collector:4317",
			wantProto:   ProtocolGRPC,
			wantHeaders: map[string]string{"a": "1", "b": "3", "c": "4"},
		},
		{
			name:    "unsupported scheme is an error",
			cfg:     Config{Endpoint: "ftp://collector:4317"},
			sig:     signalTraces,
			wantErr: true,
		},
		{
			name:    "missing host is an error",
			cfg:     Config{Endpoint: "http:///v1/traces"},
			sig:     signalTraces,
			wantErr: true,
		},
		{
			name:    "unparsable endpoint is an error",
			cfg:     Config{Endpoint: "http://bad host:4317"},
			sig:     signalTraces,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, ok, err := resolveExporterTarget(tt.cfg, tt.sig, envMap(tt.env), slog.New(slog.DiscardHandler))
			if tt.wantErr {
				require.Error(t, err)
				assert.False(t, ok)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.wantOK, ok)
			if !ok {
				return
			}
			assert.Equal(t, tt.wantURL, got.URL)
			assert.Equal(t, tt.wantProto, got.Protocol)
			if tt.wantHeaders != nil {
				assert.Equal(t, tt.wantHeaders, got.Headers)
			} else {
				assert.NotNil(t, got.Headers)
			}
		})
	}
}

func TestResolveExporterTargetDoesNotMutateConfigHeaders(t *testing.T) {
	t.Parallel()

	cfg := Config{Endpoint: "http://c:4317", Headers: map[string]string{"a": "1"}}
	env := envMap(map[string]string{"OTEL_EXPORTER_OTLP_LOGS_HEADERS": "a=2"})

	got, ok, err := resolveExporterTarget(cfg, signalLogs, env, slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, "2", got.Headers["a"])
	assert.Equal(t, "1", cfg.Headers["a"])
}

func TestNormalizeProtocolWarns(t *testing.T) {
	t.Parallel()

	tests := []struct {
		in       string
		want     string
		wantWarn bool
	}{
		{in: "", want: ProtocolGRPC},
		{in: "grpc", want: ProtocolGRPC},
		{in: " GRPC ", want: ProtocolGRPC},
		{in: "http/protobuf", want: ProtocolHTTPProtobuf},
		{in: "http/json", want: ProtocolHTTPProtobuf, wantWarn: true},
		{in: "thrift", want: ProtocolGRPC, wantWarn: true},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			t.Parallel()

			var buf bytes.Buffer
			got := normalizeProtocol(tt.in, slog.New(slog.NewTextHandler(&buf, nil)))
			assert.Equal(t, tt.want, got)
			assert.Equal(t, tt.wantWarn, buf.Len() > 0)
		})
	}
}

func TestParseHeaders(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    string
		expected map[string]string
	}{
		{name: "empty string", input: "", expected: map[string]string{}},
		{name: "single header", input: "key=value", expected: map[string]string{"key": "value"}},
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
		{name: "header with equals in value", input: "auth=token=abc123", expected: map[string]string{"auth": "token=abc123"}},
		{
			name:     "invalid header without equals",
			input:    "key1=value1,invalidheader,key2=value2",
			expected: map[string]string{"key1": "value1", "key2": "value2"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.expected, parseHeaders(tt.input))
		})
	}
}
