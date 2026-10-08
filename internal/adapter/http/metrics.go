package httpadapter

import (
	"net/http"

	"github.com/junglegaming/backend-challenge-go/internal/observability"
)

type MetricsHandler struct {
	metrics *observability.Metrics
	handler http.Handler
}

func NewMetricsHandler(metrics *observability.Metrics) *MetricsHandler {
	return &MetricsHandler{metrics: metrics, handler: metrics.Handler()}
}
func (h *MetricsHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) { h.handler.ServeHTTP(w, r) }
