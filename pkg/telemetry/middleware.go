// Package telemetry provides OpenTelemetry initialization and configuration.
package telemetry

import (
	"log/slog"
	"net/http"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.opentelemetry.io/otel/trace"
)

// HTTPMiddleware returns HTTP middleware that creates spans for each request.
func HTTPMiddleware(next http.Handler, logger *slog.Logger, httpMetrics *HTTPMetrics) http.Handler {
	tracer := otel.Tracer("dapr-mcp-server")
	prop := otel.GetTextMapPropagator()

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()

		// Track in-flight requests
		if httpMetrics != nil {
			httpMetrics.StartRequest()
			defer httpMetrics.EndRequest()
		}

		// Extract trace context from incoming headers
		ctx := prop.Extract(r.Context(), propagation.HeaderCarrier(r.Header))

		// Create span with HTTP semantic conventions
		spanName := r.Method + " " + r.URL.Path
		ctx, span := tracer.Start(ctx, spanName,
			trace.WithSpanKind(trace.SpanKindServer),
			trace.WithAttributes(
				semconv.HTTPRequestMethodKey.String(r.Method),
				semconv.URLPath(r.URL.Path),
				semconv.ServerAddress(r.Host),
				semconv.UserAgentOriginal(r.UserAgent()),
			),
		)
		defer span.End()

		// Debug log incoming request
		if logger != nil {
			logger.Debug("incoming HTTP request",
				"method", r.Method,
				"path", r.URL.Path,
				"query", r.URL.RawQuery,
				"remote_addr", r.RemoteAddr,
				"user_agent", r.UserAgent(),
				"content_length", r.ContentLength,
			)
		}

		// Wrap response writer to capture status code
		wrapper := NewResponseWriterWrapper(w)

		// Inject trace context into response headers
		prop.Inject(ctx, propagation.HeaderCarrier(wrapper.Header()))

		// Serve request
		next.ServeHTTP(wrapper, r.WithContext(ctx))

		// Calculate duration
		durationMs := float64(time.Since(start).Milliseconds())

		// Record HTTP metrics
		if httpMetrics != nil {
			httpMetrics.RecordRequest(r.Method, r.URL.Path, wrapper.StatusCode, durationMs)
		}

		// Debug log response
		if logger != nil {
			logger.Debug("HTTP response sent",
				"method", r.Method,
				"path", r.URL.Path,
				"status", wrapper.StatusCode,
				"bytes", wrapper.Written,
				"duration_ms", durationMs,
			)
		}

		// Record response attributes
		span.SetAttributes(
			semconv.HTTPResponseStatusCode(wrapper.StatusCode),
			attribute.Int64("http.response.body.size", wrapper.Written),
		)

		// Set span status based on HTTP status code
		if wrapper.StatusCode >= 400 {
			span.SetStatus(codes.Error, http.StatusText(wrapper.StatusCode))
		}
	})
}

// ResponseWriterWrapper wraps http.ResponseWriter to capture status code.
type ResponseWriterWrapper struct {
	http.ResponseWriter
	StatusCode int
	Written    int64
}

// NewResponseWriterWrapper creates a new ResponseWriterWrapper.
func NewResponseWriterWrapper(w http.ResponseWriter) *ResponseWriterWrapper {
	return &ResponseWriterWrapper{
		ResponseWriter: w,
		StatusCode:     http.StatusOK,
	}
}

// WriteHeader captures the status code.
func (w *ResponseWriterWrapper) WriteHeader(statusCode int) {
	w.StatusCode = statusCode
	w.ResponseWriter.WriteHeader(statusCode)
}

// Write captures the number of bytes written.
func (w *ResponseWriterWrapper) Write(b []byte) (int, error) {
	n, err := w.ResponseWriter.Write(b)
	w.Written += int64(n)
	return n, err
}

// Unwrap returns the original ResponseWriter.
func (w *ResponseWriterWrapper) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}
