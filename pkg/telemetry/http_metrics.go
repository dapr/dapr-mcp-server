package telemetry

import (
	"context"
	"fmt"
	"net/http"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

const (
	attrHTTPMethod     = "http.request.method"
	attrHTTPRoute      = "http.route"
	attrHTTPStatusCode = "http.response.status_code"

	// methodOther replaces non-standard HTTP methods, per the HTTP semantic conventions,
	// so arbitrary client input cannot create new series.
	methodOther = "_OTHER"
)

// knownHTTPMethods are the methods recorded as-is; anything else becomes methodOther.
var knownHTTPMethods = map[string]struct{}{
	http.MethodConnect: {}, http.MethodDelete: {}, http.MethodGet: {},
	http.MethodHead: {}, http.MethodOptions: {}, http.MethodPatch: {},
	http.MethodPost: {}, http.MethodPut: {}, http.MethodTrace: {},
}

// HTTPMetrics provides metrics instrumentation for HTTP requests.
// A nil *HTTPMetrics is valid and records nothing.
type HTTPMetrics struct {
	requestsTotal    metric.Int64Counter
	requestDuration  metric.Float64Histogram
	requestsInFlight metric.Int64UpDownCounter
}

// NewHTTPMetrics creates a new HTTPMetrics instance on the global meter provider.
func NewHTTPMetrics() (*HTTPMetrics, error) {
	return newHTTPMetrics(otel.Meter(instrumentationName))
}

func newHTTPMetrics(meter metric.Meter) (*HTTPMetrics, error) {
	requestsTotal, err := meter.Int64Counter(
		"dapr-mcp-server.http.requests_total",
		metric.WithDescription("Total number of HTTP requests"),
		metric.WithUnit("{request}"),
	)
	if err != nil {
		return nil, fmt.Errorf("create http requests counter: %w", err)
	}

	requestDuration, err := meter.Float64Histogram(
		"dapr-mcp-server.http.request_duration",
		metric.WithDescription("HTTP request duration in milliseconds, excluding event streams"),
		metric.WithUnit("ms"),
		metric.WithExplicitBucketBoundaries(1, 5, 10, 25, 50, 100, 250, 500, 1000, 2500, 5000, 10000),
	)
	if err != nil {
		return nil, fmt.Errorf("create http duration histogram: %w", err)
	}

	requestsInFlight, err := meter.Int64UpDownCounter(
		"dapr-mcp-server.http.requests_in_flight",
		metric.WithDescription("Number of HTTP requests currently being processed"),
		metric.WithUnit("{request}"),
	)
	if err != nil {
		return nil, fmt.Errorf("create http in-flight counter: %w", err)
	}

	return &HTTPMetrics{
		requestsTotal:    requestsTotal,
		requestDuration:  requestDuration,
		requestsInFlight: requestsInFlight,
	}, nil
}

// RecordRequest records a completed HTTP request.
// route must be a bounded route template such as "/" or "/items/{id}",
// never the raw request path; pass "" when no route matched.
func (m *HTTPMetrics) RecordRequest(method, route string, statusCode int, durationMs float64) {
	m.recordRequest(context.Background(), method, route, statusCode, durationMs, true)
}

// recordRequest counts the request and, when recordDuration is set, records its duration.
func (m *HTTPMetrics) recordRequest(ctx context.Context, method, route string, statusCode int, durationMs float64, recordDuration bool) {
	if m == nil {
		return
	}
	attrs := make([]attribute.KeyValue, 0, 3)
	attrs = append(attrs,
		attribute.String(attrHTTPMethod, normalizeMethod(method)),
		attribute.Int(attrHTTPStatusCode, statusCode),
	)
	if route != "" {
		attrs = append(attrs, attribute.String(attrHTTPRoute, route))
	}
	opt := metric.WithAttributes(attrs...)
	m.requestsTotal.Add(ctx, 1, opt)
	if recordDuration {
		m.requestDuration.Record(ctx, durationMs, opt)
	}
}

// StartRequest increments in-flight counter.
func (m *HTTPMetrics) StartRequest() {
	if m == nil {
		return
	}
	m.requestsInFlight.Add(context.Background(), 1)
}

// EndRequest decrements in-flight counter.
func (m *HTTPMetrics) EndRequest() {
	if m == nil {
		return
	}
	m.requestsInFlight.Add(context.Background(), -1)
}

func normalizeMethod(method string) string {
	if _, ok := knownHTTPMethods[method]; ok {
		return method
	}
	return methodOther
}
