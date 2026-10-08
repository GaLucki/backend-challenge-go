package sqs

import (
	"context"
	"errors"
	"testing"

	awssqs "github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/junglegaming/backend-challenge-go/internal/application/financial"
	"github.com/junglegaming/backend-challenge-go/internal/config"
	"github.com/junglegaming/backend-challenge-go/internal/observability"
)

func sqsCounter(t *testing.T, m *observability.Metrics, outcome string) float64 {
	t.Helper()
	fs, err := m.Registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range fs {
		if f.GetName() == "wagering_sqs_events_total" {
			for _, metric := range f.Metric {
				if metric.Label[0].GetValue() == outcome {
					return metric.GetCounter().GetValue()
				}
			}
		}
	}
	return 0
}

func TestConsumerMetricsAckFailuresDuplicatesAndPolling(t *testing.T) {
	for _, tc := range []struct {
		name, outcome string
		process, ack  error
		metric        string
	}{{"success", "PROCESSED", nil, nil, "completed"}, {"duplicate", "duplicate", nil, nil, "duplicate"}, {"transient", "", financial.ErrPersistence, nil, "transient_failure"}, {"permanent", "", financial.ErrDeliveryIntegrity, nil, "permanent_failure"}, {"ack error", "PROCESSED", nil, errors.New("SECRET broker error"), "delete_failure"}} {
		t.Run(tc.name, func(t *testing.T) {
			m := observability.NewMetrics(config.Config{MetricsEnabled: true})
			api := fakeAPI{delete: func(context.Context, *awssqs.DeleteMessageInput) error { return tc.ack }}
			c, err := NewConsumer(api, processorFunc(func(ctx context.Context, _ financial.DeliveryInput) (financial.DeliveryResult, error) {
				corr, ok := observability.CorrelationIDFromContext(ctx)
				if !ok || corr != "correlation" {
					t.Error("correlation lost")
				}
				return financial.DeliveryResult{Outcome: tc.outcome}, tc.process
			}), unitOptions(), quietLogger())
			if err != nil {
				t.Fatal(err)
			}
			c.WithTelemetry(m)
			_ = c.Handle(context.Background(), unitMessage())
			if sqsCounter(t, m, tc.metric) != 1 {
				t.Fatal("metric not recorded", tc.metric)
			}
			if (tc.process != nil || tc.ack != nil) && sqsCounter(t, m, "completed") != 0 {
				t.Fatal("completion before durable processing + ACK")
			}
		})
	}
	m := observability.NewMetrics(config.Config{MetricsEnabled: true})
	c, err := NewConsumer(fakeAPI{receive: func(context.Context) (*awssqs.ReceiveMessageOutput, error) { return nil, errors.New("broker down") }}, nil, unitOptions(), quietLogger())
	if err != nil {
		t.Fatal(err)
	}
	c.WithTelemetry(m)
	_, _ = c.Receive(context.Background())
	if sqsCounter(t, m, "poll_failure") != 1 {
		t.Fatal("poll failure not observed")
	}
}
