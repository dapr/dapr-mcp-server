package telemetry

import (
	"bufio"
	"context"
	"fmt"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"strings"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.opentelemetry.io/otel/trace"
)

const (
	attrHTTPResponseBodySize = "http.response.body.size"
	mediaTypeEventStream     = "text/event-stream"
)

// HTTPMiddleware returns HTTP middleware that creates a server span and records
// metrics for each request.
//
// Spans and metrics are labeled with the route pattern the ServeMux matched
// (http.Request.Pattern), never the raw path, so arbitrary paths cannot create
// unbounded series. Requests with no matched pattern carry no route label.
// The raw path is kept only as the span's url.path attribute.
//
// Server-sent event streams are counted but left out of the duration histogram,
// since their duration is the client's session length rather than latency.
func HTTPMiddleware(next http.Handler, logger *slog.Logger, httpMetrics *HTTPMetrics) http.Handler {
	return httpMiddleware(next, logger, httpMetrics, otel.GetTracerProvider(), otel.GetTextMapPropagator())
}

func httpMiddleware(next http.Handler, logger *slog.Logger, httpMetrics *HTTPMetrics, tp trace.TracerProvider, prop propagation.TextMapPropagator) http.Handler {
	tracer := tp.Tracer(instrumentationName)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		method := normalizeMethod(r.Method)

		httpMetrics.StartRequest()
		defer httpMetrics.EndRequest()

		ctx := prop.Extract(r.Context(), propagation.HeaderCarrier(r.Header))
		ctx, span := tracer.Start(ctx, method,
			trace.WithSpanKind(trace.SpanKindServer),
			trace.WithAttributes(
				semconv.HTTPRequestMethodKey.String(method),
				semconv.URLPath(r.URL.Path),
				semconv.ServerAddress(r.Host),
				semconv.UserAgentOriginal(r.UserAgent()),
			),
		)
		defer span.End()

		if logger != nil {
			logger.Debug("incoming HTTP request",
				"method", r.Method,
				"path", r.URL.Path,
				"remote_addr", r.RemoteAddr,
				"user_agent", r.UserAgent(),
				"content_length", r.ContentLength,
			)
		}

		wrapper := NewResponseWriterWrapper(w)
		prop.Inject(ctx, propagation.HeaderCarrier(wrapper.Header()))
		req := r.WithContext(ctx)

		defer func() {
			recovered := recover()

			status := wrapper.StatusCode
			if recovered != nil && !wrapper.wroteHeader {
				status = http.StatusInternalServerError
			}
			durationMs := float64(time.Since(start)) / float64(time.Millisecond)
			// A ServeMux wrapped by this middleware sets Pattern on req;
			// one that registered this middleware set it on r.
			route := routeFromPattern(firstNonEmpty(req.Pattern, r.Pattern))
			streaming := isEventStream(wrapper.Header())

			httpMetrics.recordRequest(context.WithoutCancel(ctx), r.Method, route, status, durationMs, !streaming)

			if route != "" {
				span.SetName(method + " " + route)
				span.SetAttributes(semconv.HTTPRoute(route))
			}
			span.SetAttributes(
				semconv.HTTPResponseStatusCode(status),
				attribute.Int64(attrHTTPResponseBodySize, wrapper.Written),
			)
			switch {
			case recovered != nil:
				span.SetStatus(codes.Error, fmt.Sprintf("panic: %v", recovered))
			case status >= http.StatusInternalServerError:
				// Per the HTTP semantic conventions, 4xx is the client's error and leaves a server span unset.
				span.SetStatus(codes.Error, http.StatusText(status))
			}

			if logger != nil {
				logger.Debug("HTTP response sent",
					"method", r.Method,
					"path", r.URL.Path,
					"status", status,
					"bytes", wrapper.Written,
					"duration_ms", durationMs,
				)
			}

			if recovered != nil {
				panic(recovered)
			}
		}()

		next.ServeHTTP(wrapper, req)
	})
}

// routeFromPattern returns the path part of a ServeMux pattern
// ("[METHOD ][HOST]/[PATH]"), or "" when there is none.
func routeFromPattern(pattern string) string {
	if i := strings.IndexByte(pattern, '/'); i >= 0 {
		return pattern[i:]
	}
	return ""
}

func isEventStream(h http.Header) bool {
	mediaType, _, err := mime.ParseMediaType(h.Get("Content-Type"))
	return err == nil && mediaType == mediaTypeEventStream
}

// ResponseWriterWrapper wraps http.ResponseWriter to capture the status code
// and the number of body bytes written.
type ResponseWriterWrapper struct {
	http.ResponseWriter
	StatusCode  int
	Written     int64
	wroteHeader bool
}

// NewResponseWriterWrapper creates a new ResponseWriterWrapper.
func NewResponseWriterWrapper(w http.ResponseWriter) *ResponseWriterWrapper {
	return &ResponseWriterWrapper{
		ResponseWriter: w,
		StatusCode:     http.StatusOK,
	}
}

// WriteHeader captures the status code. Like net/http, it ignores every call
// after the first final status; informational 1xx headers pass through.
func (w *ResponseWriterWrapper) WriteHeader(statusCode int) {
	if w.wroteHeader {
		return
	}
	if statusCode >= 100 && statusCode < 200 && statusCode != http.StatusSwitchingProtocols {
		w.ResponseWriter.WriteHeader(statusCode)
		return
	}
	w.wroteHeader = true
	w.StatusCode = statusCode
	w.ResponseWriter.WriteHeader(statusCode)
}

// Write captures the number of bytes written. Writing before WriteHeader implies 200.
func (w *ResponseWriterWrapper) Write(b []byte) (int, error) {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	n, err := w.ResponseWriter.Write(b)
	w.Written += int64(n)
	return n, err
}

// Flush implements http.Flusher by delegating to the underlying ResponseWriter.
// This is critical for SSE (text/event-stream) responses used by the MCP
// Streamable HTTP transport. Without this, Go buffers the entire response,
// sets Content-Length and Connection: keep-alive, which causes MCP clients
// to block waiting for more events that never arrive.
// It does nothing when the underlying writer cannot flush.
func (w *ResponseWriterWrapper) Flush() {
	if err := http.NewResponseController(w.ResponseWriter).Flush(); err == nil {
		w.wroteHeader = true
	}
}

// Hijack implements http.Hijacker by delegating to the underlying ResponseWriter.
// It returns an error wrapping http.ErrNotSupported when that writer cannot be hijacked.
func (w *ResponseWriterWrapper) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	conn, rw, err := http.NewResponseController(w.ResponseWriter).Hijack()
	if err != nil {
		return nil, nil, fmt.Errorf("hijack: %w", err)
	}
	return conn, rw, nil
}

// Unwrap returns the original ResponseWriter.
func (w *ResponseWriterWrapper) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}
