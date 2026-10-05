package httpadapter

import (
	"net/http"
	"sync/atomic"
)

// HealthHandler exposes liveness and readiness probes.
type HealthHandler struct {
	ready *atomic.Bool
}

// NewHealthHandler creates a health handler. Readiness becomes true after
// the HTTP server lifecycle reports a successful start.
func NewHealthHandler(ready *atomic.Bool) *HealthHandler {
	return &HealthHandler{ready: ready}
}

type healthResponse struct {
	Status string `json:"status"`
}

// Live handles GET /health/live.
func (h *HealthHandler) Live(w http.ResponseWriter, _ *http.Request) {
	WriteJSON(w, http.StatusOK, healthResponse{Status: "ok"})
}

// Ready handles GET /health/ready.
// In phase 0 this only confirms the application finished initialization.
func (h *HealthHandler) Ready(w http.ResponseWriter, _ *http.Request) {
	if h.ready == nil || !h.ready.Load() {
		WriteJSON(w, http.StatusServiceUnavailable, healthResponse{Status: "not_ready"})
		return
	}

	WriteJSON(w, http.StatusOK, healthResponse{Status: "ok"})
}
