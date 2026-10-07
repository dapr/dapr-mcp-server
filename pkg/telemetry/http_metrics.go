package telemetry

import (
	"context"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// HTTPMetrics provides metrics instrumentation for HTTP requests.
type HTTPMetrics struct {
	requestsTotal    metric.Int64Counter
	requestDuration  metric.Float64Histogram
	requestsInFlight metric.Int64UpDownCounter
}

// NewHTTPMetrics creates a new HTTPMetrics instance.
func NewHTTPMetrics() (*HTTPMetrics, error) {
	meter := otel.Meter("dapr-mcp-server")

	requestsTotal, err := meter.Int64Counter(
		"dapr-mcp-server.http.requests_total",
		metric.WithDescription("Total number of HTTP requests"),
		metric.WithUnit("{request}"),
	)
	if err != nil {
		return nil, err
	}

	requestDuration, err := meter.Float64Histogram(
		"dapr-mcp-server.http.request_duration",
		metric.WithDescription("HTTP request duration in milliseconds"),
		metric.WithUnit("ms"),
		metric.WithExplicitBucketBoundaries(1, 5, 10, 25, 50, 100, 250, 500, 1000, 2500, 5000, 10000),
	)
	if err != nil {
		return nil, err
	}

	requestsInFlight, err := meter.Int64UpDownCounter(
		"dapr-mcp-server.http.requests_in_flight",
		metric.WithDescription("Number of HTTP requests currently being processed"),
		metric.WithUnit("{request}"),
	)
	if err != nil {
		return nil, err
	}

	return &HTTPMetrics{
		requestsTotal:    requestsTotal,
		requestDuration:  requestDuration,
		requestsInFlight: requestsInFlight,
	}, nil
}

// RecordRequest records an HTTP request with its attributes.
func (m *HTTPMetrics) RecordRequest(method, path string, statusCode int, durationMs float64) {
	attrs := []attribute.KeyValue{
		attribute.String("http.request.method", method),
		attribute.String("url.path", path),
		attribute.Int("http.response.status_code", statusCode),
	}
	m.requestsTotal.Add(context.Background(), 1, metric.WithAttributes(attrs...))
	m.requestDuration.Record(context.Background(), durationMs, metric.WithAttributes(attrs...))
}

// StartRequest increments in-flight counter.
func (m *HTTPMetrics) StartRequest() {
	m.requestsInFlight.Add(context.Background(), 1)
}

// EndRequest decrements in-flight counter.
func (m *HTTPMetrics) EndRequest() {
	m.requestsInFlight.Add(context.Background(), -1)
}
