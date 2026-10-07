// Package health provides health check endpoints for Kubernetes probes.
package health

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"sync/atomic"
	"time"

	dapr "github.com/dapr/go-sdk/client"
)

// Status represents the health status of a component.
type Status string

const (
	// StatusHealthy means the component is working and can serve traffic.
	StatusHealthy Status = "healthy"
	// StatusUnhealthy means the component is not working and the server should not receive traffic.
	StatusUnhealthy Status = "unhealthy"
	// StatusDegraded means the component works with reduced functionality.
	StatusDegraded Status = "degraded"
)

// Paths the probe handlers are served on by RegisterHandlers.
const (
	LivenessPath  = "/livez"
	ReadinessPath = "/readyz"
	StartupPath   = "/startupz"
)

const (
	daprCheckTimeout = 5 * time.Second

	componentDapr   = "dapr"
	componentServer = "server"

	msgServerNotReady      = "server not ready"
	msgDaprNotInitialized  = "dapr client not initialized"
	msgDaprUnreachable     = "dapr sidecar unreachable"
	msgDaprSidecarHealthy  = "sidecar connected"
	contentTypeHeader      = "Content-Type"
	contentTypeApplication = "application/json"
)

// CheckResult represents the result of a health check.
type CheckResult struct {
	Status    Status `json:"status"`
	Component string `json:"component,omitempty"`
	Message   string `json:"message,omitempty"`
	Latency   string `json:"latency,omitempty"`
}

// HealthResponse is the response from health endpoints.
type HealthResponse struct {
	Status  Status        `json:"status"`
	Checks  []CheckResult `json:"checks,omitempty"`
	Version string        `json:"version,omitempty"`
}

// Handler provides health check HTTP handlers.
type Handler struct {
	daprClient  dapr.Client
	logger      *slog.Logger
	version     string
	ready       atomic.Bool
	startupDone atomic.Bool
}

// NewHandler creates a health handler that reports neither started nor ready.
// Call SetStartupDone and SetReady once initialization has finished.
// A nil logger falls back to slog.Default.
func NewHandler(daprClient dapr.Client, version string, logger *slog.Logger) *Handler {
	if logger == nil {
		logger = slog.Default()
	}
	return &Handler{
		daprClient: daprClient,
		logger:     logger,
		version:    version,
	}
}

// SetReady sets the readiness state.
func (h *Handler) SetReady(ready bool) {
	h.ready.Store(ready)
}

// SetStartupDone sets the startup completion state.
func (h *Handler) SetStartupDone(done bool) {
	h.startupDone.Store(done)
}

// LivenessHandler handles /livez requests.
// Liveness probes should be simple - just check if the server is running.
func (h *Handler) LivenessHandler(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, HealthResponse{
		Status:  StatusHealthy,
		Version: h.version,
	})
}

// ReadinessHandler handles /readyz requests.
// The server is ready only when it is marked ready and the Dapr sidecar answers,
// since no tool can work without the sidecar.
func (h *Handler) ReadinessHandler(w http.ResponseWriter, r *http.Request) {
	checks := make([]CheckResult, 0)
	overallStatus := StatusHealthy

	if !h.ready.Load() {
		overallStatus = StatusUnhealthy
		checks = append(checks, CheckResult{
			Status:    StatusUnhealthy,
			Component: componentServer,
			Message:   msgServerNotReady,
		})
	}

	if h.daprClient != nil {
		daprCheck := h.checkDapr(r.Context())
		checks = append(checks, daprCheck)
		if daprCheck.Status != StatusHealthy {
			overallStatus = StatusUnhealthy
		}
	}

	httpStatus := http.StatusOK
	if overallStatus != StatusHealthy {
		httpStatus = http.StatusServiceUnavailable
	}

	writeJSON(w, httpStatus, HealthResponse{
		Status:  overallStatus,
		Checks:  checks,
		Version: h.version,
	})
}

// StartupHandler handles /startupz requests.
// Startup probes check if the application has finished initialization.
func (h *Handler) StartupHandler(w http.ResponseWriter, _ *http.Request) {
	status := StatusHealthy
	httpStatus := http.StatusOK

	if !h.startupDone.Load() {
		status = StatusUnhealthy
		httpStatus = http.StatusServiceUnavailable
	}

	writeJSON(w, httpStatus, HealthResponse{
		Status:  status,
		Version: h.version,
	})
}

// checkDapr checks the Dapr sidecar connectivity.
// The underlying error is logged rather than returned,
// because probe responses are served without authentication.
func (h *Handler) checkDapr(ctx context.Context) CheckResult {
	if h.daprClient == nil {
		return CheckResult{
			Status:    StatusUnhealthy,
			Component: componentDapr,
			Message:   msgDaprNotInitialized,
		}
	}

	start := time.Now()
	ctx, cancel := context.WithTimeout(ctx, daprCheckTimeout)
	defer cancel()

	_, err := h.daprClient.GetMetadata(ctx)
	latency := time.Since(start)

	if err != nil {
		h.log().Warn("Dapr sidecar health check failed", "error", err, "latency", latency)
		return CheckResult{
			Status:    StatusUnhealthy,
			Component: componentDapr,
			Message:   msgDaprUnreachable,
			Latency:   latency.String(),
		}
	}

	return CheckResult{
		Status:    StatusHealthy,
		Component: componentDapr,
		Message:   msgDaprSidecarHealthy,
		Latency:   latency.String(),
	}
}

// RegisterHandlers registers the liveness, readiness and startup handlers with the given mux.
func (h *Handler) RegisterHandlers(mux *http.ServeMux) {
	mux.HandleFunc(LivenessPath, h.LivenessHandler)
	mux.HandleFunc(ReadinessPath, h.ReadinessHandler)
	mux.HandleFunc(StartupPath, h.StartupHandler)
}

func (h *Handler) log() *slog.Logger {
	if h.logger == nil {
		return slog.Default()
	}
	return h.logger
}

func writeJSON(w http.ResponseWriter, status int, resp HealthResponse) {
	w.Header().Set(contentTypeHeader, contentTypeApplication)
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(resp)
}
