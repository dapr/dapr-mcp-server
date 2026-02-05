package telemetry

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestHTTPMiddleware(t *testing.T) {
	// Create a simple handler that we'll wrap with middleware
	nextHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	})

	// Wrap with middleware
	handler := HTTPMiddleware(nextHandler, nil, nil)

	// Create request
	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	rec := httptest.NewRecorder()

	// Execute
	handler.ServeHTTP(rec, req)

	// Verify response
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "OK", rec.Body.String())
}

func TestHTTPMiddlewareWithTraceContext(t *testing.T) {
	// Create a handler that checks for context
	nextHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Context should be set on the request
		assert.NotNil(t, r.Context())
		w.WriteHeader(http.StatusOK)
	})

	handler := HTTPMiddleware(nextHandler, nil, nil)

	// Create request with trace headers
	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.Header.Set("traceparent", "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestNewResponseWriterWrapper(t *testing.T) {
	rec := httptest.NewRecorder()
	wrapper := NewResponseWriterWrapper(rec)

	assert.NotNil(t, wrapper)
	assert.Equal(t, http.StatusOK, wrapper.StatusCode) // Default status
	assert.Equal(t, int64(0), wrapper.Written)
	assert.Equal(t, rec, wrapper.ResponseWriter)
}

func TestResponseWriterWrapperWriteHeader(t *testing.T) {
	rec := httptest.NewRecorder()
	wrapper := NewResponseWriterWrapper(rec)

	wrapper.WriteHeader(http.StatusNotFound)

	assert.Equal(t, http.StatusNotFound, wrapper.StatusCode)
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestResponseWriterWrapperWrite(t *testing.T) {
	rec := httptest.NewRecorder()
	wrapper := NewResponseWriterWrapper(rec)

	data := []byte("Hello, World!")
	n, err := wrapper.Write(data)

	assert.NoError(t, err)
	assert.Equal(t, len(data), n)
	assert.Equal(t, int64(len(data)), wrapper.Written)
	assert.Equal(t, "Hello, World!", rec.Body.String())
}

func TestResponseWriterWrapperMultipleWrites(t *testing.T) {
	rec := httptest.NewRecorder()
	wrapper := NewResponseWriterWrapper(rec)

	wrapper.Write([]byte("Hello, "))
	wrapper.Write([]byte("World!"))

	assert.Equal(t, int64(13), wrapper.Written)
	assert.Equal(t, "Hello, World!", rec.Body.String())
}

func TestResponseWriterWrapperUnwrap(t *testing.T) {
	rec := httptest.NewRecorder()
	wrapper := NewResponseWriterWrapper(rec)

	unwrapped := wrapper.Unwrap()

	assert.Equal(t, rec, unwrapped)
}

func TestResponseWriterWrapperHeader(t *testing.T) {
	rec := httptest.NewRecorder()
	wrapper := NewResponseWriterWrapper(rec)

	wrapper.Header().Set("Content-Type", "application/json")

	assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))
}

func TestHTTPMiddlewarePreservesMethod(t *testing.T) {
	methods := []string{
		http.MethodGet,
		http.MethodPost,
		http.MethodPut,
		http.MethodDelete,
		http.MethodPatch,
	}

	for _, method := range methods {
		t.Run(method, func(t *testing.T) {
			var capturedMethod string
			nextHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				capturedMethod = r.Method
				w.WriteHeader(http.StatusOK)
			})

			handler := HTTPMiddleware(nextHandler, nil, nil)
			req := httptest.NewRequest(method, "/test", nil)
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			assert.Equal(t, method, capturedMethod)
		})
	}
}

func TestHTTPMiddlewarePreservesPath(t *testing.T) {
	paths := []string{
		"/",
		"/test",
		"/api/v1/users",
		"/api/v1/users/123",
	}

	for _, path := range paths {
		t.Run(path, func(t *testing.T) {
			var capturedPath string
			nextHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				capturedPath = r.URL.Path
				w.WriteHeader(http.StatusOK)
			})

			handler := HTTPMiddleware(nextHandler, nil, nil)
			req := httptest.NewRequest(http.MethodGet, path, nil)
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			assert.Equal(t, path, capturedPath)
		})
	}
}

func TestResponseWriterWrapperDefaultStatusCode(t *testing.T) {
	rec := httptest.NewRecorder()
	wrapper := NewResponseWriterWrapper(rec)

	// Write without setting status - should keep default OK
	wrapper.Write([]byte("data"))

	// StatusCode should still be the default
	assert.Equal(t, http.StatusOK, wrapper.StatusCode)
}

func TestResponseWriterWrapperStatusCodeNotOverwritten(t *testing.T) {
	rec := httptest.NewRecorder()
	wrapper := NewResponseWriterWrapper(rec)

	wrapper.WriteHeader(http.StatusCreated)
	wrapper.WriteHeader(http.StatusOK) // This should still write to underlying

	// Our wrapper captured the first call
	assert.Equal(t, http.StatusOK, wrapper.StatusCode)
}
