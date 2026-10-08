package financial

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/junglegaming/backend-challenge-go/internal/domain/wager"
	"github.com/junglegaming/backend-challenge-go/internal/ports"
)

// Attach once during construction, before serving requests/starting workers.
func (s *Service) WithTelemetry(t ports.Telemetry, logger *slog.Logger) *Service {
	s.metrics = t
	s.logger = logger
	return s
}
func (s *Service) telemetry() ports.Telemetry {
	if s.metrics == nil {
		return ports.NoopTelemetry{}
	}
	return s.metrics
}

func (s *Service) observeFinancial(ctx context.Context, in WagerInput, r WagerResult, err error, started time.Time, outcome string, messageID ...string) {
	t := s.telemetry()
	if err != nil {
		outcome = "error"
	}
	t.Count("financial_attempts_total", string(in.Type), outcome)
	t.Duration("financial_processing_duration_seconds", time.Since(started), string(in.Type))
	if err == nil && outcome == "new" {
		t.Count("financial_operations_total", string(in.Type), string(r.State))
		if r.State == wager.StateProcessed && in.Type != wager.TypeLoss {
			t.Count("financial_movements_total", string(in.Type))
		}
	}
	switch {
	case outcome == "replay":
		t.Count("idempotency_events_total", "replay")
	case outcome == "duplicate":
		t.Count("idempotency_events_total", "duplicate")
	case outcome == "external_duplicate":
		t.Count("idempotency_events_total", "external_duplicate")
	case errors.Is(err, ErrIdempotencyConflict):
		t.Count("idempotency_events_total", "idempotency_conflict")
	case errors.Is(err, ErrDuplicateExternalTransaction), errors.Is(err, ErrExternalPayloadConflict):
		t.Count("idempotency_events_total", "external_conflict")
	case errors.Is(err, ErrDeliveryIntegrity):
		t.Count("idempotency_events_total", "inbox_hash_conflict")
	}
	if errors.Is(err, ErrPersistence) {
		t.Count("dependency_failures_total", "postgres", "financial")
	}
	if s.logger != nil {
		status := string(r.State)
		if err != nil {
			status = "ERROR"
		}
		attrs := []any{"correlationId", in.CorrelationID, "transactionId", r.TransactionID, "walletId", in.WalletID, "providerId", in.ProviderID, "operationType", in.Type, "status", status, "outcome", outcome, "durationMs", time.Since(started).Milliseconds()}
		if len(messageID) > 0 {
			attrs = append(attrs, "messageId", messageID[0], "component", "inbox")
		}
		s.logger.InfoContext(ctx, "financial attempt completed", attrs...)
	}
}
