package observability

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/junglegaming/backend-challenge-go/internal/config"
)

func TestPrivateRegistryCountersHistogramsBoundedLabelsAndConcurrentUpdates(t *testing.T) {
	m := NewMetrics(config.Config{MetricsEnabled: true})
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			m.Count("financial_operations_total", "BET", "PROCESSED")
			m.Duration("financial_processing_duration_seconds", time.Millisecond, "BET")
		}()
	}
	wg.Wait()
	m.Count("http_requests_total", "ATTACKER-ID", "/wallets/secret-wallet-id", "bad")
	m.Count("financial_operations_total", "secret-provider-id", "secret-transaction-id")
	families, err := m.Registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	var counter float64
	var observations uint64
	for _, family := range families {
		for _, metric := range family.Metric {
			for _, label := range metric.Label {
				if strings.Contains(label.GetValue(), "secret") || strings.Contains(label.GetValue(), "ATTACKER") {
					t.Fatal("unbounded label", label)
				}
			}
			if family.GetName() == "wagering_financial_operations_total" && metric.Label[0].GetValue() == "BET" {
				counter = metric.GetCounter().GetValue()
			}
			if family.GetName() == "wagering_financial_processing_duration_seconds" {
				observations = metric.GetHistogram().GetSampleCount()
				if metric.GetHistogram().GetSampleSum() <= 0 {
					t.Fatal("duration not observed")
				}
			}
		}
	}
	if counter != 100 || observations != 100 {
		t.Fatal(counter, observations)
	}
	other := NewMetrics(config.Config{MetricsEnabled: true})
	if other.Registry == m.Registry {
		t.Fatal("shared registry")
	}
	disabled := NewMetrics(config.Config{})
	disabled.Count("financial_operations_total", "BET", "PROCESSED")
	fs, err := disabled.Registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range fs {
		if f.GetName() == "wagering_financial_operations_total" {
			t.Fatal("disabled instrumentation recorded counters")
		}
	}
}

func TestJSONLoggingRedactsCredentialsPayloadAndRawErrors(t *testing.T) {
	var output bytes.Buffer
	logger := NewLoggerToWriter("info", &output)
	logger.InfoContext(WithCorrelationID(context.Background(), "corr"), "dependency failed", "Authorization", "Bearer SECRET_TOKEN", "clientSecret", "SECRET_CLIENT", "password", "SECRET_PASSWORD", "connectionString", "postgres://name:SECRET_DSN@host/db", "payload", "SECRET_PAYLOAD", "error", errors.New("SQL contains SECRET_ERROR"), "walletId", "wallet", "correlationId", "corr")
	for _, secret := range []string{"SECRET_TOKEN", "SECRET_CLIENT", "SECRET_PASSWORD", "SECRET_DSN", "SECRET_PAYLOAD", "SECRET_ERROR"} {
		if strings.Contains(output.String(), secret) {
			t.Fatal("sensitive log", output.String())
		}
	}
	if !strings.Contains(output.String(), `"correlationId":"corr"`) || !strings.Contains(output.String(), `"error":"dependency_failure"`) {
		t.Fatal(output.String())
	}
}
