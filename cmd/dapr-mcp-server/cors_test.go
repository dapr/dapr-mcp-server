package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
)

const testOrigin = "https://app.example.com"

func TestCORSMiddleware(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		origin     string
		method     string
		wantACAO   string
		wantStatus int
		wantNext   bool
	}{
		{name: "disabled GET", method: http.MethodGet, wantStatus: http.StatusOK, wantNext: true},
		{name: "disabled OPTIONS reaches next", method: http.MethodOptions, wantStatus: http.StatusOK, wantNext: true},
		{name: "enabled GET", origin: testOrigin, method: http.MethodGet, wantACAO: testOrigin, wantStatus: http.StatusOK, wantNext: true},
		{name: "enabled preflight", origin: testOrigin, method: http.MethodOptions, wantACAO: testOrigin, wantStatus: http.StatusNoContent},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			nextCalled := false
			next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				nextCalled = true
				w.WriteHeader(http.StatusOK)
			})
			rec := httptest.NewRecorder()

			corsMiddleware(tt.origin, next).ServeHTTP(rec, httptest.NewRequest(tt.method, "/", nil))

			assert.Equal(t, tt.wantStatus, rec.Code)
			assert.Equal(t, tt.wantNext, nextCalled)
			assert.Equal(t, tt.wantACAO, rec.Header().Get("Access-Control-Allow-Origin"))
			if tt.origin == "" {
				assert.Empty(t, rec.Header().Get("Access-Control-Allow-Methods"))
			} else {
				assert.Equal(t, corsAllowHeaders, rec.Header().Get("Access-Control-Allow-Headers"))
			}
		})
	}
}
