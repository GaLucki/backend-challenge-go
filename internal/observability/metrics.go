package observability

import (
	"net/http"
	"strings"
	"time"

	"github.com/junglegaming/backend-challenge-go/internal/config"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Each process/Fx app owns its registry. No global registry or identifiers in
// labels, and every dynamic label has a finite whitelist before vector lookup.
type Metrics struct {
	Enabled    bool
	Registry   *prometheus.Registry
	counters   map[string]*prometheus.CounterVec
	histograms map[string]*prometheus.HistogramVec
	gauges     map[string]prometheus.Gauge
	labels     map[string][]string
}

func NewMetrics(cfg config.Config) *Metrics {
	m := &Metrics{Enabled: cfg.MetricsEnabled, Registry: prometheus.NewRegistry(), counters: map[string]*prometheus.CounterVec{}, histograms: map[string]*prometheus.HistogramVec{}, gauges: map[string]prometheus.Gauge{}, labels: map[string][]string{}}
	m.Registry.MustRegister(collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
	for _, s := range []struct {
		name, help string
		labels     []string
	}{
		{"http_requests_total", "Completed HTTP requests, including auth failures.", []string{"method", "route", "status"}},
		{"financial_attempts_total", "Core attempts; new, replay and duplicates are distinct outcomes.", []string{"operation", "outcome"}},
		{"financial_operations_total", "New committed transactions, including rejected, pending, loss and opening; excludes replays.", []string{"operation", "state"}},
		{"financial_movements_total", "Committed balance-changing movements including opening and pending resolutions; excludes loss/replays.", []string{"operation"}},
		{"idempotency_events_total", "Replays and detected transport/inbox/external identity conflicts.", []string{"outcome"}},
		{"sqs_events_total", "Consumer observations; completed means durable processing plus successful DeleteMessage.", []string{"outcome"}},
		{"outbox_events_total", "Publisher observations; published means sender accepted and durable published mark confirmed.", []string{"outcome"}},
		{"pending_events_total", "Committed pending retries/resolutions/expirations and worker failures; excludes idle polls.", []string{"outcome"}},
		{"reconciliation_runs_total", "Audits by result, with technical errors separate.", []string{"result"}},
		{"reconciliation_divergences_total", "Detected divergence occurrences, not distinct wallets.", []string{"code"}},
		{"dependency_failures_total", "Observed dependency failures with bounded component names.", []string{"dependency", "component"}},
	} {
		v := prometheus.NewCounterVec(prometheus.CounterOpts{Namespace: "wagering", Name: s.name, Help: s.help}, s.labels)
		m.Registry.MustRegister(v)
		m.counters[s.name] = v
		m.labels[s.name] = s.labels
	}
	for _, s := range []struct {
		name, help string
		labels     []string
	}{
		{"http_request_duration_seconds", "HTTP request duration including authentication.", []string{"method", "route"}},
		{"financial_processing_duration_seconds", "Core attempt duration including SQL waits, replays and errors.", []string{"operation"}},
		{"sqs_processing_duration_seconds", "Consumer processing duration excluding DeleteMessage.", nil},
		{"outbox_publication_duration_seconds", "Publication claim duration including ownership and durable result.", nil},
		{"pending_processing_duration_seconds", "Pending resolution attempt duration, including idle polls.", nil},
		{"reconciliation_duration_seconds", "Full audit duration.", []string{"result"}},
	} {
		v := prometheus.NewHistogramVec(prometheus.HistogramOpts{Namespace: "wagering", Name: s.name, Help: s.help, Buckets: []float64{.005, .025, .1, .5, 1, 2.5, 5, 10, 30}}, s.labels)
		m.Registry.MustRegister(v)
		m.histograms[s.name] = v
		m.labels[s.name] = s.labels
	}
	for _, s := range []struct{ name, help string }{
		{"http_in_flight", "Active HTTP requests."}, {"sqs_in_flight", "Active consumer processing calls."},
		{"outbox_pending", "Unpublished persisted events (global database backlog)."},
		{"outbox_retry_scheduled", "Unpublished events with retries scheduled in the future."},
		{"outbox_lag_seconds", "Age of oldest unpublished event, including future retries; zero for empty backlog."},
		{"pending_references", "Uncompleted persisted pending references (global database backlog)."},
		{"dlq_depth_approximate", "SQS DLQ approximate visible plus not-visible message count; redrive is owned by SQS."},
		{"operational_collection_success", "Last database backlog collection succeeded (1) or failed (0)."},
		{"operational_collection_timestamp_seconds", "Unix time of last successful database backlog collection."},
		{"dlq_collection_success", "Last DLQ attribute collection succeeded (1) or failed (0)."},
		{"dlq_collection_enabled", "DLQ polling configured for this instance (1) or disabled (0)."},
		{"dlq_collection_timestamp_seconds", "Unix time of last successful DLQ attribute collection."},
		{"ready", "Last readiness probe result; not a background dependency probe."},
	} {
		v := prometheus.NewGauge(prometheus.GaugeOpts{Namespace: "wagering", Name: s.name, Help: s.help})
		m.Registry.MustRegister(v)
		m.gauges[s.name] = v
	}
	return m
}

func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.Registry, promhttp.HandlerOpts{EnableOpenMetrics: true})
}
func (m *Metrics) Count(name string, values ...string) {
	if m == nil || !m.Enabled {
		return
	}
	if v := m.counters[name]; v != nil && len(values) == len(m.labels[name]) {
		v.WithLabelValues(m.normalize(name, values)...).Inc()
	}
}
func (m *Metrics) Duration(name string, d time.Duration, values ...string) {
	if m == nil || !m.Enabled {
		return
	}
	if v := m.histograms[name]; v != nil && len(values) == len(m.labels[name]) {
		v.WithLabelValues(m.normalize(name, values)...).Observe(max(0, d.Seconds()))
	}
}
func (m *Metrics) InFlight(name string, delta int) {
	if m == nil || !m.Enabled {
		return
	}
	if g := m.gauges[name]; g != nil {
		g.Add(float64(delta))
	}
}
func (m *Metrics) Set(name string, value float64) {
	if m == nil || !m.Enabled {
		return
	}
	if g := m.gauges[name]; g != nil {
		g.Set(value)
	}
}

func (m *Metrics) normalize(name string, values []string) []string {
	result := make([]string, len(values))
	for i, v := range values {
		result[i] = boundedLabel(m.labels[name][i], v)
	}
	return result
}
func boundedLabel(label, value string) string {
	var allowed string
	switch label {
	case "operation":
		allowed = "OPENING BET WIN LOSS REFUND ROLLBACK"
	case "state":
		allowed = "PENDING PENDING_REFERENCE PROCESSED REJECTED FAILED"
	case "result":
		allowed = "CONSISTENT DIVERGENT ERROR"
	case "method":
		allowed = "GET HEAD POST PUT PATCH DELETE OPTIONS CONNECT TRACE"
	case "dependency":
		allowed = "postgres sqs oidc"
	case "component":
		allowed = "financial readiness outbox consumer pending metrics"
	case "outcome":
		allowed = "new replay duplicate external_duplicate error idempotency_conflict external_conflict inbox_hash_conflict received completed transient_failure permanent_failure delete_failure poll_failure published publish_failure retry_scheduled lease_lost mark_failure store_failure retry resolved expired rejected"
	case "code":
		allowed = "BALANCE_MISMATCH LEDGER_CHAIN_BROKEN LEDGER_AMOUNT_MISMATCH LEDGER_DIRECTION_MISMATCH LEDGER_CURRENCY_MISMATCH LEDGER_TRANSACTION_NOT_FOUND TRANSACTION_LEDGER_MISSING UNEXPECTED_LEDGER_ENTRY NEGATIVE_RECONSTRUCTED_BALANCE WALLET_VERSION_MISMATCH TRANSACTION_WALLET_MISMATCH DUPLICATE_LEDGER_ENTRY LEDGER_ARITHMETIC_OVERFLOW TRANSACTION_INVALID"
	case "route":
		for _, route := range []string{"/wallets", "/wallets/{walletId}", "/wallets/{walletId}/ledger", "/wallets/{walletId}/reconciliation", "/wagering/transactions", "/wagering/transactions/{transactionId}", "/providers/{providerId}/wagering/transactions/{externalTransactionId}", "/health/live", "/health/ready", "/metrics", "/auth/me", "/auth/internal", "/auth/providers/{providerId}"} {
			if value == route {
				return value
			}
		}
		return "unmatched"
	case "status":
		if len(value) == 3 && value[0] >= '1' && value[0] <= '5' && value[1] >= '0' && value[1] <= '9' && value[2] >= '0' && value[2] <= '9' {
			return value
		}
		return "other"
	}
	for _, v := range strings.Fields(allowed) {
		if value == v {
			return value
		}
	}
	return "other"
}
