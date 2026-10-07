package telemetry

import (
	"fmt"
	"log/slog"
	"maps"
	"net/url"
	"strconv"
	"strings"
)

// OTLP protocols accepted in Config.Protocol and the OTEL_EXPORTER_OTLP_*PROTOCOL variables.
const (
	ProtocolGRPC         = "grpc"
	ProtocolHTTPProtobuf = "http/protobuf"
	ProtocolHTTPJSON     = "http/json"
)

const (
	envOTLPPrefix = "OTEL_EXPORTER_OTLP_"

	envSuffixEndpoint = "ENDPOINT"
	envSuffixProtocol = "PROTOCOL"
	envSuffixInsecure = "INSECURE"
	envSuffixHeaders  = "HEADERS"

	envOTLPEndpoint = envOTLPPrefix + envSuffixEndpoint
	envOTLPProtocol = envOTLPPrefix + envSuffixProtocol
	envOTLPInsecure = envOTLPPrefix + envSuffixInsecure
	envOTLPHeaders  = envOTLPPrefix + envSuffixHeaders

	schemeHTTP  = "http"
	schemeHTTPS = "https"
)

// signal identifies one OTLP signal and the defaults the spec assigns it.
type signal struct {
	name    string
	envName string
	urlPath string
}

var (
	signalTraces  = signal{name: "traces", envName: "TRACES", urlPath: "/v1/traces"}
	signalMetrics = signal{name: "metrics", envName: "METRICS", urlPath: "/v1/metrics"}
	signalLogs    = signal{name: "logs", envName: "LOGS", urlPath: "/v1/logs"}
)

// env returns the signal-specific variable name for suffix,
// for example OTEL_EXPORTER_OTLP_TRACES_ENDPOINT.
func (s signal) env(suffix string) string {
	return envOTLPPrefix + s.envName + "_" + suffix
}

// exporterTarget is the resolved destination of one signal's exporter.
type exporterTarget struct {
	// Protocol is ProtocolGRPC or ProtocolHTTPProtobuf.
	Protocol string
	// URL is the absolute endpoint. Its scheme selects TLS (https) or plaintext (http).
	// For HTTP it carries the full signal path; for gRPC the path is empty.
	URL     string
	Headers map[string]string
}

// resolveExporterTarget works out where one signal is exported, following the
// OTLP exporter spec: signal-specific variables override the generic Config
// values, a generic endpoint gets the signal path appended for HTTP, and a
// signal-specific endpoint is used as-is unless it has no path at all.
// It returns ok=false when no endpoint is configured for the signal.
func resolveExporterTarget(cfg Config, sig signal, getenv func(string) string, logger *slog.Logger) (exporterTarget, bool, error) {
	endpoint := strings.TrimSpace(getenv(sig.env(envSuffixEndpoint)))
	signalSpecific := endpoint != ""
	if !signalSpecific {
		endpoint = strings.TrimSpace(cfg.Endpoint)
	}
	if endpoint == "" {
		return exporterTarget{}, false, nil
	}

	protocol := normalizeProtocol(firstNonEmpty(getenv(sig.env(envSuffixProtocol)), cfg.Protocol), logger)

	insecure := cfg.Insecure
	if raw := getenv(sig.env(envSuffixInsecure)); raw != "" {
		insecure = parseBoolEnv(sig.env(envSuffixInsecure), raw, cfg.Insecure, logger)
	}

	u, err := parseEndpoint(endpoint, insecure)
	if err != nil {
		return exporterTarget{}, false, fmt.Errorf("%s endpoint: %w", sig.name, err)
	}

	switch {
	case protocol == ProtocolGRPC:
		u.Path, u.RawPath = "", ""
	case signalSpecific:
		if u.Path == "" || u.Path == "/" {
			u.Path, u.RawPath = sig.urlPath, ""
		}
	default:
		u.Path, u.RawPath = strings.TrimSuffix(u.Path, "/")+sig.urlPath, ""
	}

	headers := maps.Clone(cfg.Headers)
	if headers == nil {
		headers = map[string]string{}
	}
	maps.Copy(headers, parseHeaders(getenv(sig.env(envSuffixHeaders))))

	return exporterTarget{Protocol: protocol, URL: u.String(), Headers: headers}, true, nil
}

// parseEndpoint parses an endpoint into an absolute http(s) URL.
// An endpoint without a scheme, such as "collector:4317",
// gets https unless insecure is set.
func parseEndpoint(endpoint string, insecure bool) (*url.URL, error) {
	if !strings.Contains(endpoint, "://") {
		scheme := schemeHTTPS
		if insecure {
			scheme = schemeHTTP
		}
		endpoint = scheme + "://" + endpoint
	}

	u, err := url.Parse(endpoint)
	if err != nil {
		return nil, fmt.Errorf("parse %q: %w", endpoint, err)
	}
	if u.Scheme != schemeHTTP && u.Scheme != schemeHTTPS {
		return nil, fmt.Errorf("unsupported scheme %q in %q, want http or https", u.Scheme, endpoint)
	}
	if u.Host == "" {
		return nil, fmt.Errorf("missing host in %q", endpoint)
	}
	u.RawQuery, u.Fragment = "", ""
	return u, nil
}

// normalizeProtocol maps a configured protocol onto one the exporters support,
// warning and falling back when it cannot.
func normalizeProtocol(protocol string, logger *slog.Logger) string {
	switch p := strings.ToLower(strings.TrimSpace(protocol)); p {
	case "", ProtocolGRPC:
		return ProtocolGRPC
	case ProtocolHTTPProtobuf:
		return ProtocolHTTPProtobuf
	case ProtocolHTTPJSON:
		logger.Warn("OTLP http/json is not supported by the Go exporters, using http/protobuf", "protocol", protocol)
		return ProtocolHTTPProtobuf
	default:
		logger.Warn("unsupported OTLP protocol, using grpc", "protocol", protocol)
		return ProtocolGRPC
	}
}

// parseBoolEnv parses a boolean variable, logging and returning def when it is invalid.
func parseBoolEnv(name, raw string, def bool, logger *slog.Logger) bool {
	v, err := strconv.ParseBool(strings.TrimSpace(raw))
	if err != nil {
		logger.Warn("ignoring invalid boolean environment variable", "name", name, "value", raw)
		return def
	}
	return v
}

// parseHeaders parses the OTEL_EXPORTER_OTLP_HEADERS format: comma-separated key=value pairs.
func parseHeaders(headersStr string) map[string]string {
	headers := make(map[string]string)
	if headersStr == "" {
		return headers
	}
	for pair := range strings.SplitSeq(headersStr, ",") {
		if k, v, ok := strings.Cut(pair, "="); ok {
			headers[strings.TrimSpace(k)] = strings.TrimSpace(v)
		}
	}
	return headers
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
