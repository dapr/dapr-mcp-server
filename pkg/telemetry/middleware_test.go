package telemetry

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

const (
	metricHTTPRequests = "dapr-mcp-server.http.requests_total"
	metricHTTPDuration = "dapr-mcp-server.http.request_duration"
	metricHTTPInFlight = "dapr-mcp-server.http.requests_in_flight"
)

// middlewareHarness wires httpMiddleware to in-memory span and metric readers.
type middlewareHarness struct {
	spans  *tracetest.SpanRecorder
	reader *sdkmetric.ManualReader
	logs   *bytes.Buffer
}

func newMiddlewareHarness(t *testing.T, next http.Handler) (http.Handler, *middlewareHarness) {
	t.Helper()
	spans := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spans))
	mp, reader := newManualMeterProvider(t)
	metrics, err := newHTTPMetrics(mp.Meter("test"))
	require.NoError(t, err)

	logs := &bytes.Buffer{}
	logger := slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	prop := propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}, propagation.Baggage{})

	return httpMiddleware(next, logger, metrics, tp, prop), &middlewareHarness{spans: spans, reader: reader, logs: logs}
}

func (h *middlewareHarness) onlySpan(t *testing.T) sdktrace.ReadOnlySpan {
	t.Helper()
	ended := h.spans.Ended()
	require.Len(t, ended, 1)
	return ended[0]
}

func spanAttr(span sdktrace.ReadOnlySpan, key string) (attribute.Value, bool) {
	for _, kv := range span.Attributes() {
		if string(kv.Key) == key {
			return kv.Value, true
		}
	}
	return attribute.Value{}, false
}

func serve(handler http.Handler, method, target string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(method, target, nil))
	return rec
}

func TestHTTPMiddlewareStatusAndSpan(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		handler        http.HandlerFunc
		wantStatus     int
		wantSpanStatus codes.Code
		wantBytes      int64
	}{
		{
			name:           "explicit 200",
			handler:        func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) },
			wantStatus:     http.StatusOK,
			wantSpanStatus: codes.Unset,
		},
		{
			name:           "implicit 200 from write",
			handler:        func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("hello")) },
			wantStatus:     http.StatusOK,
			wantSpanStatus: codes.Unset,
			wantBytes:      5,
		},
		{
			name:           "4xx leaves the server span unset",
			handler:        func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNotFound) },
			wantStatus:     http.StatusNotFound,
			wantSpanStatus: codes.Unset,
		},
		{
			name:           "5xx marks the span as error",
			handler:        func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusBadGateway) },
			wantStatus:     http.StatusBadGateway,
			wantSpanStatus: codes.Error,
		},
		{
			name: "second WriteHeader is ignored",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusCreated)
				w.WriteHeader(http.StatusInternalServerError)
			},
			wantStatus:     http.StatusCreated,
			wantSpanStatus: codes.Unset,
		},
		{
			name: "write then WriteHeader keeps 200",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte("x"))
				w.WriteHeader(http.StatusInternalServerError)
			},
			wantStatus:     http.StatusOK,
			wantSpanStatus: codes.Unset,
			wantBytes:      1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			handler, h := newMiddlewareHarness(t, tt.handler)
			rec := serve(handler, http.MethodGet, "/x")

			assert.Equal(t, tt.wantStatus, rec.Code)
			span := h.onlySpan(t)
			assert.Equal(t, tt.wantSpanStatus, span.Status().Code)
			status, _ := spanAttr(span, "http.response.status_code")
			assert.Equal(t, int64(tt.wantStatus), status.AsInt64())
			size, _ := spanAttr(span, attrHTTPResponseBodySize)
			assert.Equal(t, tt.wantBytes, size.AsInt64())

			points := sumPoints(t, collectMetric(t, h.reader, metricHTTPRequests))
			require.Len(t, points, 1)
			code, _ := points[0].Attributes.Value(attrHTTPStatusCode)
			assert.Equal(t, int64(tt.wantStatus), code.AsInt64())
		})
	}
}

func TestHTTPMiddlewareRouteLabelIsBounded(t *testing.T) {
	t.Parallel()

	// Registered under "/" the way the server wires it: the mux sets r.Pattern before the middleware runs.
	inner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	mw, h := newMiddlewareHarness(t, inner)
	mux := http.NewServeMux()
	mux.Handle("/", mw)

	const distinctPaths = 100
	for i := range distinctPaths {
		serve(mux, http.MethodGet, fmt.Sprintf("/session/%d?token=secret", i))
	}

	points := sumPoints(t, collectMetric(t, h.reader, metricHTTPRequests))
	require.Len(t, points, 1, "distinct paths must not create distinct series")
	assert.Equal(t, int64(distinctPaths), points[0].Value)
	route, ok := points[0].Attributes.Value(attrHTTPRoute)
	require.True(t, ok)
	assert.Equal(t, "/", route.AsString())
	_, hasPath := points[0].Attributes.Value("url.path")
	assert.False(t, hasPath)

	spans := h.spans.Ended()
	require.Len(t, spans, distinctPaths)
	assert.Equal(t, "GET /", spans[0].Name())
	path, _ := spanAttr(spans[0], "url.path")
	assert.Equal(t, "/session/0", path.AsString(), "raw path stays on the span")

	assert.NotContains(t, h.logs.String(), "secret", "query strings must not be logged")
}

func TestHTTPMiddlewareWrappingMuxUsesMatchedPattern(t *testing.T) {
	t.Parallel()

	mux := http.NewServeMux()
	mux.HandleFunc("GET /items/{id}", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	handler, h := newMiddlewareHarness(t, mux)

	for i := range 20 {
		serve(handler, http.MethodGet, fmt.Sprintf("/items/%d", i))
	}
	for i := range 20 {
		serve(handler, http.MethodGet, fmt.Sprintf("/unknown/%d", i))
	}

	points := sumPoints(t, collectMetric(t, h.reader, metricHTTPRequests))
	require.Len(t, points, 2, "one matched route and one unmatched series")
	for _, p := range points {
		route, hasRoute := p.Attributes.Value(attrHTTPRoute)
		code, _ := p.Attributes.Value(attrHTTPStatusCode)
		switch code.AsInt64() {
		case http.StatusOK:
			assert.Equal(t, "/items/{id}", route.AsString())
		case http.StatusNotFound:
			assert.False(t, hasRoute)
		default:
			t.Fatalf("unexpected status %d", code.AsInt64())
		}
	}

	names := map[string]int{}
	for _, s := range h.spans.Ended() {
		names[s.Name()]++
	}
	assert.Equal(t, map[string]int{"GET /items/{id}": 20, "GET": 20}, names)
}

func TestHTTPMiddlewareUnknownMethodIsBounded(t *testing.T) {
	t.Parallel()

	handler, h := newMiddlewareHarness(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	serve(handler, "BREW", "/")
	serve(handler, "PURGE", "/")

	points := sumPoints(t, collectMetric(t, h.reader, metricHTTPRequests))
	require.Len(t, points, 1)
	method, _ := points[0].Attributes.Value(attrHTTPMethod)
	assert.Equal(t, methodOther, method.AsString())
}

func TestHTTPMiddlewarePanicRecordsAndRepanics(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		handler    http.HandlerFunc
		wantStatus int64
	}{
		{
			name:       "panic before writing records 500",
			handler:    func(http.ResponseWriter, *http.Request) { panic("boom") },
			wantStatus: http.StatusInternalServerError,
		},
		{
			name: "panic after writing keeps the sent status",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusAccepted)
				panic("boom")
			},
			wantStatus: http.StatusAccepted,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			handler, h := newMiddlewareHarness(t, tt.handler)
			assert.PanicsWithValue(t, "boom", func() { serve(handler, http.MethodPost, "/") })

			span := h.onlySpan(t)
			assert.Equal(t, codes.Error, span.Status().Code)
			assert.Contains(t, span.Status().Description, "boom")

			points := sumPoints(t, collectMetric(t, h.reader, metricHTTPRequests))
			require.Len(t, points, 1)
			code, _ := points[0].Attributes.Value(attrHTTPStatusCode)
			assert.Equal(t, tt.wantStatus, code.AsInt64())

			inFlight := sumPoints(t, collectMetric(t, h.reader, metricHTTPInFlight))
			require.Len(t, inFlight, 1)
			assert.Equal(t, int64(0), inFlight[0].Value)

			assert.Len(t, histogramPoints(t, collectMetric(t, h.reader, metricHTTPDuration)), 1)
		})
	}
}

func TestHTTPMiddlewareEventStreamSkipsDuration(t *testing.T) {
	t.Parallel()

	handler, h := newMiddlewareHarness(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
	}))
	serve(handler, http.MethodGet, "/")

	points := sumPoints(t, collectMetric(t, h.reader, metricHTTPRequests))
	require.Len(t, points, 1)
	assert.Equal(t, int64(1), points[0].Value)
	assert.False(t, hasMetric(t, h.reader, metricHTTPDuration), "event streams stay out of the latency histogram")
}

func TestHTTPMiddlewarePropagatesTraceContext(t *testing.T) {
	t.Parallel()

	const traceID = "0af7651916cd43dd8448eb211c80319c"
	handler, h := newMiddlewareHarness(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("traceparent", "00-"+traceID+"-b7ad6b7169203331-01")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	span := h.onlySpan(t)
	assert.Equal(t, traceID, span.SpanContext().TraceID().String())
	assert.Contains(t, rec.Header().Get("traceparent"), traceID)
}

func TestHTTPMiddlewareNilLoggerAndMetrics(t *testing.T) {
	t.Parallel()

	handler := HTTPMiddleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("OK"))
	}), nil, nil)

	rec := serve(handler, http.MethodGet, "/test")

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "OK", rec.Body.String())
}

func TestHTTPMiddlewarePreservesRequest(t *testing.T) {
	t.Parallel()

	for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodDelete} {
		for _, path := range []string{"/", "/api/v1/users/123"} {
			t.Run(method+path, func(t *testing.T) {
				t.Parallel()

				var gotMethod, gotPath string
				handler := HTTPMiddleware(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
					gotMethod, gotPath = r.Method, r.URL.Path
				}), nil, nil)
				serve(handler, method, path)

				assert.Equal(t, method, gotMethod)
				assert.Equal(t, path, gotPath)
			})
		}
	}
}

func TestRouteFromPattern(t *testing.T) {
	t.Parallel()

	tests := map[string]string{
		"":                          "",
		"/":                         "/",
		"/items/{id}":               "/items/{id}",
		"GET /items/{id}":           "/items/{id}",
		"example.com/":              "/",
		"POST example.com/a/{b...}": "/a/{b...}",
		"GET":                       "",
	}
	for pattern, want := range tests {
		assert.Equal(t, want, routeFromPattern(pattern), pattern)
	}
}

func TestNewResponseWriterWrapper(t *testing.T) {
	t.Parallel()

	rec := httptest.NewRecorder()
	wrapper := NewResponseWriterWrapper(rec)

	assert.Equal(t, http.StatusOK, wrapper.StatusCode)
	assert.Equal(t, int64(0), wrapper.Written)
	assert.Equal(t, rec, wrapper.Unwrap())
}

func TestResponseWriterWrapperWrites(t *testing.T) {
	t.Parallel()

	rec := httptest.NewRecorder()
	wrapper := NewResponseWriterWrapper(rec)
	wrapper.Header().Set("Content-Type", "text/plain")

	_, err := wrapper.Write([]byte("Hello, "))
	require.NoError(t, err)
	_, err = wrapper.Write([]byte("World!"))
	require.NoError(t, err)

	assert.Equal(t, int64(13), wrapper.Written)
	assert.Equal(t, "Hello, World!", rec.Body.String())
	assert.Equal(t, "text/plain", rec.Header().Get("Content-Type"))
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestResponseWriterWrapperInformationalPassesThrough(t *testing.T) {
	t.Parallel()

	rec := httptest.NewRecorder()
	wrapper := NewResponseWriterWrapper(rec)

	wrapper.WriteHeader(http.StatusEarlyHints)
	wrapper.WriteHeader(http.StatusTeapot)

	assert.Equal(t, http.StatusTeapot, wrapper.StatusCode, "1xx does not count as the final status")
	assert.True(t, wrapper.wroteHeader)
}

func TestResponseWriterWrapperFlush(t *testing.T) {
	t.Parallel()

	rec := httptest.NewRecorder()
	wrapper := NewResponseWriterWrapper(rec)

	wrapper.Flush()

	assert.True(t, rec.Flushed)
	wrapper.WriteHeader(http.StatusTeapot)
	assert.Equal(t, http.StatusOK, wrapper.StatusCode, "flushing commits the implicit 200")
}

// plainWriter is a ResponseWriter that supports neither flushing nor hijacking.
type plainWriter struct{ header http.Header }

func (p *plainWriter) Header() http.Header         { return p.header }
func (p *plainWriter) Write(b []byte) (int, error) { return len(b), nil }
func (p *plainWriter) WriteHeader(int)             {}

func TestResponseWriterWrapperWithoutOptionalInterfaces(t *testing.T) {
	t.Parallel()

	wrapper := NewResponseWriterWrapper(&plainWriter{header: http.Header{}})

	assert.NotPanics(t, wrapper.Flush)
	_, _, err := wrapper.Hijack()
	assert.ErrorIs(t, err, http.ErrNotSupported)
}

// hijackableWriter is a ResponseWriter whose connection can be hijacked.
type hijackableWriter struct {
	*httptest.ResponseRecorder
	conn net.Conn
}

func (h *hijackableWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return h.conn, bufio.NewReadWriter(bufio.NewReader(h.conn), bufio.NewWriter(h.conn)), nil
}

func TestResponseWriterWrapperHijack(t *testing.T) {
	t.Parallel()

	server, client := net.Pipe()
	t.Cleanup(func() {
		_ = server.Close()
		_ = client.Close()
	})

	wrapper := NewResponseWriterWrapper(&hijackableWriter{ResponseRecorder: httptest.NewRecorder(), conn: server})
	conn, rw, err := wrapper.Hijack()

	require.NoError(t, err)
	assert.Same(t, server, conn)
	assert.NotNil(t, rw)
	assert.False(t, errors.Is(err, http.ErrNotSupported))
}

func TestHTTPMetricsRecordRequest(t *testing.T) {
	t.Parallel()

	mp, reader := newManualMeterProvider(t)
	m, err := newHTTPMetrics(mp.Meter("test"))
	require.NoError(t, err)

	m.StartRequest()
	m.RecordRequest(http.MethodPost, "/", http.StatusAccepted, 3.5)
	m.RecordRequest(http.MethodPost, "", http.StatusNotFound, 1)
	m.EndRequest()

	points := sumPoints(t, collectMetric(t, reader, metricHTTPRequests))
	require.Len(t, points, 2)
	dur := histogramPoints(t, collectMetric(t, reader, metricHTTPDuration))
	require.Len(t, dur, 2)
	inFlight := sumPoints(t, collectMetric(t, reader, metricHTTPInFlight))
	require.Len(t, inFlight, 1)
	assert.Equal(t, int64(0), inFlight[0].Value)
}

func TestNewHTTPMetrics(t *testing.T) {
	m, err := NewHTTPMetrics()
	require.NoError(t, err)
	assert.NotNil(t, m)
}

func TestNilHTTPMetricsIsNoOp(t *testing.T) {
	t.Parallel()

	var m *HTTPMetrics
	assert.NotPanics(t, func() {
		m.StartRequest()
		m.RecordRequest(http.MethodGet, "/", http.StatusOK, 1)
		m.EndRequest()
	})
}
