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
func (s *Service) ResolvePendingOnce(ctx context.Context, now time.Time) (bool, error) {
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
		tx, err := r.Wagers.Get(ctx, pending.TransactionID)
		if err != nil {
			return err
		}
		if tx.State() != wager.StatePendingReference {
			return ErrPersistence
		}
		w, err := r.Wallets.GetForUpdate(ctx, wallet.ID(tx.WalletID()))
		if err != nil {
			return err
		}
		before, version := w.Balance(), w.Version()
		pending.LastAttemptAt = &now
		var direction ports.LedgerDirection
		var changed, missing bool
		if !now.Before(pending.ExpiresAt) || pending.AttemptCount >= pending.MaxAttempts {
			err = tx.MarkRejected(FailureReferenceNotFound, now)
		} else {
			pending.AttemptCount++
			direction, changed, missing, err = s.applyReversal(ctx, r, &tx, &w, now)
		}
		if err != nil {
			return err
		}
		if missing {
			if pending.AttemptCount >= pending.MaxAttempts {
				if err = tx.MarkRejected(FailureReferenceNotFound, now); err != nil {
					return err
				}
			} else {
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
