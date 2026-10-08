package financial

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/junglegaming/backend-challenge-go/internal/domain/wager"
	"github.com/junglegaming/backend-challenge-go/internal/domain/wallet"
	"github.com/junglegaming/backend-challenge-go/internal/ports"
)

type PendingPolicy struct {
	BaseDelay   time.Duration
	MaxDelay    time.Duration
	MaxAttempts int32
	TTL         time.Duration
}

func DefaultPendingPolicy() PendingPolicy {
	return PendingPolicy{BaseDelay: time.Second, MaxDelay: time.Minute, MaxAttempts: 10, TTL: 15 * time.Minute}
}
func (p PendingPolicy) Validate() error {
	if p.BaseDelay < time.Microsecond || p.MaxDelay < p.BaseDelay || p.MaxAttempts < 1 || p.TTL < time.Microsecond {
		return fmt.Errorf("invalid pending reference policy")
	}
	return nil
}
func NewServiceWithPolicy(uow ports.UnitOfWork, policy PendingPolicy) (*Service, error) {
	if err := policy.Validate(); err != nil {
		return nil, err
	}
	s := NewService(uow)
	s.pendingPolicy = policy
	return s, nil
}
func (p PendingPolicy) backoff(attempt int32) time.Duration {
	delay := p.BaseDelay
	for n := int32(1); n < attempt && delay < p.MaxDelay; n++ {
		if delay > p.MaxDelay/2 {
			return p.MaxDelay
		}
		delay *= 2
	}
	if delay > p.MaxDelay {
		return p.MaxDelay
	}
	return delay
}

// ResolvePendingOnce claims one due row and resolves it within one SQL transaction.
// The supplied UTC instant allows deterministic batches/tests without scheduler sleeps.
func (s *Service) ResolvePendingOnce(ctx context.Context, now time.Time) (handledResult bool, resultErr error) {
	started := time.Now()
	var observed WagerResult
	var action, correlation string
	var retryCount int32
	defer func() {
		t := s.telemetry()
		t.Duration("pending_processing_duration_seconds", time.Since(started))
		if resultErr != nil {
			t.Count("pending_events_total", "error")
			if errors.Is(resultErr, ErrPersistence) {
				t.Count("dependency_failures_total", "postgres", "pending")
			}
			return
		}
		if !handledResult {
			return
		}
		t.Count("pending_events_total", action)
		if action == "resolved" {
			t.Count("financial_movements_total", string(observed.Type))
		}
		if s.logger != nil {
			s.logger.InfoContext(ctx, "pending reference attempt completed", "correlationId", correlation, "transactionId", observed.TransactionID, "walletId", observed.WalletID, "providerId", observed.ProviderID, "operationType", observed.Type, "status", observed.State, "outcome", action, "retryCount", retryCount, "durationMs", time.Since(started).Milliseconds())
		}
	}()
	now = now.UTC().Truncate(time.Microsecond)
	handled := false
	err := s.uow.WithinTransaction(ctx, func(r ports.Repositories) error {
		pending, err := r.PendingReferences.ClaimNext(ctx, now)
		if errors.Is(err, ports.ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		handled = true
		correlation = pending.CorrelationID
		retryCount = pending.AttemptCount
		tx, err := r.Wagers.Get(ctx, pending.TransactionID)
		if err != nil {
			return err
		}
		if tx.State() != wager.StatePendingReference {
			return ErrPersistence
		}
		observed = WagerResult{TransactionID: tx.ID(), WalletID: wallet.ID(tx.WalletID()), ProviderID: tx.ProviderID(), Type: tx.Type(), State: tx.State()}
		w, err := r.Wallets.GetForUpdate(ctx, wallet.ID(tx.WalletID()))
		if err != nil {
			return err
		}
		before, version := w.Balance(), w.Version()
		pending.LastAttemptAt = &now
		var direction ports.LedgerDirection
		var changed, missing bool
		if !now.Before(pending.ExpiresAt) || pending.AttemptCount >= pending.MaxAttempts {
			action = "expired"
			err = tx.MarkRejected(FailureReferenceNotFound, now)
		} else {
			pending.AttemptCount++
			retryCount = pending.AttemptCount
			direction, changed, missing, err = s.applyReversal(ctx, r, &tx, &w, now)
		}
		if err != nil {
			return err
		}
		if missing {
			if pending.AttemptCount >= pending.MaxAttempts {
				action = "expired"
				if err = tx.MarkRejected(FailureReferenceNotFound, now); err != nil {
					return err
				}
			} else {
				action = "retry"
				pending.NextAttemptAt = now.Add(s.pendingPolicy.backoff(pending.AttemptCount))
				if pending.NextAttemptAt.After(pending.ExpiresAt) {
					pending.NextAttemptAt = pending.ExpiresAt
				}
				// A retry without resolution creates no additional pending event/result.
				return r.PendingReferences.Update(ctx, pending)
			}
		}
		result, err := finishReversal(ctx, r, tx, w, before, version, direction, changed, pending.CorrelationID, wager.StatePendingReference)
		if err != nil {
			return err
		}
		observed = result
		if action != "expired" {
			action = "rejected"
			if changed {
				action = "resolved"
			}
		}
		payload, err := json.Marshal(result)
		if err != nil {
			return ErrPersistence
		}
		if err = r.Idempotency.UpdatePendingResult(ctx, tx.ID(), payload, now); err != nil {
			return err
		}
		pending.CompletedAt = &now
		return r.PendingReferences.Update(ctx, pending)
	})
	if err != nil {
		return false, applicationError(err)
	}
	return handled, nil
}
