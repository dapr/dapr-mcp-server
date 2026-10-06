package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

const handlerWait = time.Second

func TestCloseStreamsOnShutdown(t *testing.T) {
	tests := []struct {
		method       string
		wantCanceled bool
	}{
		{method: http.MethodGet, wantCanceled: true},
		{method: http.MethodPost, wantCanceled: false},
	}

	for _, tt := range tests {
		t.Run(tt.method, func(t *testing.T) {
			shutdownCtx, shutdown := context.WithCancel(context.Background())
			started := make(chan struct{})
			canceled := make(chan bool, 1)

			handler := closeStreamsOnShutdown(shutdownCtx, http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
				close(started)
				select {
				case <-r.Context().Done():
					canceled <- true
				case <-time.After(handlerWait):
					canceled <- false
				}
			}))

			go handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(tt.method, "/", nil))
			<-started
			shutdown()

			if got := <-canceled; got != tt.wantCanceled {
				t.Errorf("request context canceled = %v, want %v", got, tt.wantCanceled)
			}
		})
	}
}
