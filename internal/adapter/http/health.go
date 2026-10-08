package httpadapter

import (
	"context"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/junglegaming/backend-challenge-go/internal/config"
	"github.com/junglegaming/backend-challenge-go/internal/ports"
)

// DatabasePinger checks PostgreSQL availability for readiness probes.
type DatabasePinger interface {
	Ping(ctx context.Context) error
}

// HealthHandler exposes liveness and readiness probes.
type HealthHandler struct {
	ready         *atomic.Bool
	db            DatabasePinger
	broker        DatabasePinger
	healthTimeout time.Duration
	metrics       ports.Telemetry
}

func (h *HealthHandler) WithTelemetry(t ports.Telemetry) *HealthHandler { h.metrics = t; return h }
func (h *HealthHandler) WithBroker(p DatabasePinger) *HealthHandler     { h.broker = p; return h }

// NewHealthHandler creates a health handler.
// Readiness requires application initialization and a successful database ping.
func NewHealthHandler(ready *atomic.Bool, db DatabasePinger, cfg config.Config) *HealthHandler {
	return &HealthHandler{
		ready:         ready,
		db:            db,
		healthTimeout: cfg.DBHealthTimeout,
	}
}

type healthResponse struct {
	Status string `json:"status"`
}

// Live handles GET /health/live.
func (h *HealthHandler) Live(w http.ResponseWriter, _ *http.Request) {
	WriteJSON(w, http.StatusOK, healthResponse{Status: "ok"})
}

// Ready handles GET /health/ready.
func (h *HealthHandler) Ready(w http.ResponseWriter, r *http.Request) {
	success := false
	defer func() {
		if m, ok := h.metrics.(interface{ Set(string, float64) }); ok {
			v := float64(0)
			if success {
				v = 1
			}
			m.Set("ready", v)
		}
	}()
	if h.ready == nil || !h.ready.Load() {
		WriteError(w, http.StatusServiceUnavailable, ErrorCodeNotReady, "application not ready")
		return
	}

	if h.db == nil {
		WriteError(w, http.StatusServiceUnavailable, ErrorCodeNotReady, "database unavailable")
		return
	}

	timeout := h.healthTimeout
	if timeout <= 0 {
		timeout = 2 * time.Second
	}

	ctx, cancel := context.WithTimeout(r.Context(), timeout)
	defer cancel()

	if err := h.db.Ping(ctx); err != nil {
		if h.metrics != nil {
			h.metrics.Count("dependency_failures_total", "postgres", "readiness")
		}
		WriteError(w, http.StatusServiceUnavailable, ErrorCodeNotReady, "database unavailable")
		return
	}
	if h.broker != nil {
		if err := h.broker.Ping(ctx); err != nil {
			if h.metrics != nil {
				h.metrics.Count("dependency_failures_total", "sqs", "readiness")
			}
			WriteError(w, http.StatusServiceUnavailable, ErrorCodeNotReady, "broker unavailable")
			return
		}
	}

	success = true
	WriteJSON(w, http.StatusOK, healthResponse{Status: "ok"})
}
