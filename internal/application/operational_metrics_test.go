package application

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/junglegaming/backend-challenge-go/internal/config"
	"github.com/junglegaming/backend-challenge-go/internal/observability"
	"github.com/junglegaming/backend-challenge-go/internal/ports"
	"go.uber.org/fx/fxtest"
)

type operationalReadFunc func(context.Context) (ports.OperationalSnapshot, error)

func (f operationalReadFunc) ReadOperationalSnapshot(ctx context.Context) (ports.OperationalSnapshot, error) {
	return f(ctx)
}
func operationalMetric(t *testing.T, m *observability.Metrics, name string) float64 {
	t.Helper()
	fs, err := m.Registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range fs {
		if f.GetName() == "wagering_"+name && len(f.Metric) > 0 {
			return f.Metric[0].GetGauge().GetValue()
		}
	}
	t.Fatal("metric missing", name)
	return 0
}

func TestOperationalCollectionKeepsLastSampleAndReportsFailures(t *testing.T) {
	m := observability.NewMetrics(config.Config{MetricsEnabled: true})
	failure := false
	c := &OperationalCollector{reader: operationalReadFunc(func(context.Context) (ports.OperationalSnapshot, error) {
		if failure {
			return ports.OperationalSnapshot{}, errors.New("SECRET SQL error")
		}
		return ports.OperationalSnapshot{OutboxPending: 3, OutboxRetryScheduled: 1, OutboxLag: 5 * time.Second, PendingReferences: 2}, nil
	}), metrics: m, cfg: config.Config{MetricsCollectTimeout: time.Second}, logger: slog.New(slog.NewJSONHandler(io.Discard, nil)), dlq: func(context.Context) (int64, error) {
		if failure {
			return 0, errors.New("SQS down")
		}
		return 4, nil
	}}
	if err := c.CollectOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if operationalMetric(t, m, "outbox_pending") != 3 || operationalMetric(t, m, "dlq_depth_approximate") != 4 || operationalMetric(t, m, "operational_collection_success") != 1 {
		t.Fatal("snapshot missing")
	}
	failure = true
	if err := c.CollectOnce(context.Background()); err == nil {
		t.Fatal("expected failure")
	}
	if operationalMetric(t, m, "outbox_pending") != 3 || operationalMetric(t, m, "dlq_depth_approximate") != 4 || operationalMetric(t, m, "operational_collection_success") != 0 || operationalMetric(t, m, "dlq_collection_success") != 0 {
		t.Fatal("outage fabricated empty backlog")
	}
}

func TestOperationalLifecycleCancelsAndJoinsCollector(t *testing.T) {
	started := make(chan struct{})
	stopped := make(chan struct{})
	c := &OperationalCollector{reader: operationalReadFunc(func(ctx context.Context) (ports.OperationalSnapshot, error) {
		close(started)
		<-ctx.Done()
		defer close(stopped)
		return ports.OperationalSnapshot{}, ctx.Err()
	}), metrics: observability.NewMetrics(config.Config{MetricsEnabled: true}), cfg: config.Config{MetricsCollectInterval: time.Second, MetricsCollectTimeout: 500 * time.Millisecond}, logger: slog.New(slog.NewJSONHandler(io.Discard, nil))}
	lc := fxtest.NewLifecycle(t)
	RegisterOperationalCollector(lc, c)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := lc.Start(ctx); err != nil {
		t.Fatal(err)
	}
	<-started
	if err := lc.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-stopped:
	default:
		t.Fatal("collector goroutine orphaned")
	}
}
