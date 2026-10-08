package postgres

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/junglegaming/backend-challenge-go/internal/ports"
	"github.com/prometheus/client_golang/prometheus"
)

type operationalReader struct{ pool *pgxpool.Pool }

func NewOperationalReader(pool *pgxpool.Pool) ports.OperationalReader {
	return &operationalReader{pool: pool}
}
func (r *operationalReader) ReadOperationalSnapshot(ctx context.Context) (ports.OperationalSnapshot, error) {
	var s ports.OperationalSnapshot
	var oldest *time.Time
	var at time.Time
	err := r.pool.QueryRow(ctx, `SELECT count(*),count(*) FILTER(WHERE retry_count>0 AND next_attempt_at>statement_timestamp()),min(occurred_at),statement_timestamp(),(SELECT count(*) FROM pending_wager_references WHERE completed_at IS NULL) FROM outbox_events WHERE published_at IS NULL`).Scan(&s.OutboxPending, &s.OutboxRetryScheduled, &oldest, &at, &s.PendingReferences)
	if err != nil {
		return ports.OperationalSnapshot{}, mapError(err)
	}
	if oldest != nil {
		s.OutboxLag = max(0, at.Sub(*oldest))
	}
	return s, nil
}

type poolCollector struct {
	pool  *pgxpool.Pool
	descs []*prometheus.Desc
}

func NewPoolCollector(pool *pgxpool.Pool) prometheus.Collector {
	names := []string{"connections", "in_use", "idle", "max_connections", "acquires_total", "acquire_duration_seconds_total", "acquire_canceled_total", "acquire_empty_total"}
	c := &poolCollector{pool: pool}
	for _, n := range names {
		c.descs = append(c.descs, prometheus.NewDesc("wagering_postgres_pool_"+n, "pgxpool process-local statistic: "+n, nil, nil))
	}
	return c
}
func (c *poolCollector) Describe(ch chan<- *prometheus.Desc) {
	for _, d := range c.descs {
		ch <- d
	}
}
func (c *poolCollector) Collect(ch chan<- prometheus.Metric) {
	s := c.pool.Stat()
	values := []float64{float64(s.TotalConns()), float64(s.AcquiredConns()), float64(s.IdleConns()), float64(s.MaxConns()), float64(s.AcquireCount()), s.AcquireDuration().Seconds(), float64(s.CanceledAcquireCount()), float64(s.EmptyAcquireCount())}
	for i, v := range values {
		kind := prometheus.GaugeValue
		if i >= 4 {
			kind = prometheus.CounterValue
		}
		ch <- prometheus.MustNewConstMetric(c.descs[i], kind, v)
	}
}
