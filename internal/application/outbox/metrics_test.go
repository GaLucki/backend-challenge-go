package outbox

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
)

func publisherCounter(t *testing.T, m *observability.Metrics, outcome string) float64 {
	t.Helper()
	fs, err := m.Registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range fs {
		if f.GetName() == "wagering_outbox_events_total" {
			for _, metric := range f.Metric {
				if metric.Label[0].GetValue() == outcome {
					return metric.GetCounter().GetValue()
				}
			}
		}
	}
	return 0
}

func TestOutboxMetricsRespectConfirmedMarkAndScheduledRetry(t *testing.T) {
	for _, mode := range []string{"success", "send failure", "mark failure", "lease lost"} {
		t.Run(mode, func(t *testing.T) {
			m := observability.NewMetrics(config.Config{MetricsEnabled: true})
			store := testStore{mark: func(context.Context, ports.OutboxClaim) (bool, error) {
				if mode == "mark failure" {
					return false, errors.New("db down")
				}
				return true, nil
			}, retry: func(context.Context, ports.OutboxClaim, time.Duration) (bool, error) { return true, nil }, renew: func(context.Context, ports.OutboxClaim, time.Duration) (bool, error) {
				return mode != "lease lost", nil
			}}
			p, err := NewPublisher(store, sendFunc(func(context.Context, ports.OutboxEvent) error {
				if mode == "send failure" {
					return errors.New("broker down")
				}
				return nil
			}), testOptions(), slog.New(slog.NewJSONHandler(io.Discard, nil)))
			if err != nil {
				t.Fatal(err)
			}
			p.WithTelemetry(m)
			_ = p.PublishClaim(context.Background(), testClaim())
			want := float64(0)
			if mode == "success" {
				want = 1
			}
			if publisherCounter(t, m, "published") != want {
				t.Fatal("published counted before durable mark")
			}
			if mode == "send failure" && (publisherCounter(t, m, "retry_scheduled") != 1 || publisherCounter(t, m, "publish_failure") != 1) {
				t.Fatal("failure/retry metrics missing")
			}
		})
	}
}
