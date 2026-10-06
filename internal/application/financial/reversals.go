package financial

import (
	"context"
	"errors"
	"time"

	"github.com/junglegaming/backend-challenge-go/internal/domain/money"
	"github.com/junglegaming/backend-challenge-go/internal/domain/wager"
	"github.com/junglegaming/backend-challenge-go/internal/domain/wallet"
	"github.com/junglegaming/backend-challenge-go/internal/ports"
)

func isReversal(kind wager.Type) bool { return kind == wager.TypeRefund || kind == wager.TypeRollback }

func validateReference(tx, ref wager.Transaction) (ports.LedgerDirection, wager.FailureCode) {
	var direction ports.LedgerDirection
	switch tx.Type() {
	case wager.TypeRefund:
		if ref.Type() != wager.TypeBet {
			return "", FailureReferenceTypeInvalid
		}
		direction = ports.Credit
	case wager.TypeRollback:
		switch ref.Type() {
		case wager.TypeBet:
			direction = ports.Credit
		case wager.TypeWin, wager.TypeRefund:
			direction = ports.Debit
		default:
			return "", FailureReferenceTypeInvalid
		}
	default:
		return "", FailureReferenceTypeInvalid
	}
	if ref.ProviderID() != tx.ProviderID() {
		return "", FailureReferenceProviderMismatch
	}
	if ref.PlayerID() != tx.PlayerID() {
		return "", FailureReferencePlayerMismatch
	}
	if ref.WalletID() != tx.WalletID() {
		return "", FailureReferenceWalletMismatch
	}
	if ref.Currency() != tx.Currency() {
		return "", FailureReferenceCurrencyMismatch
	}
	if ref.RoundID() != tx.RoundID() {
		return "", FailureReferenceRoundMismatch
	}
	if ref.State() != wager.StateProcessed {
		return "", FailureReferenceStateInvalid
	}
	if !ref.Amount().Equal(tx.Amount()) {
		return "", FailureReferenceAmountMismatch
	}
	return direction, ""
}

// applyReversal is shared by first processing and pending resolution. The caller
// holds the wallet lock and has persisted tx; all writes use its current UoW.
func (s *Service) applyReversal(ctx context.Context, r ports.Repositories, tx *wager.Transaction, w *wallet.Wallet, at time.Time) (direction ports.LedgerDirection, changed, missing bool, err error) {
	reject := func(code wager.FailureCode) (ports.LedgerDirection, bool, bool, error) {
		return "", false, false, tx.MarkRejected(code, at)
	}
	if tx.ExternalTransactionID() == tx.ReferenceExternalTransactionID() {
		return reject(FailureReferenceTypeInvalid)
	}
	ref, err := r.Wagers.GetByExternalID(ctx, tx.ProviderID(), tx.ReferenceExternalTransactionID())
	if errors.Is(err, ports.ErrNotFound) {
		return "", false, true, nil
	}
	if err != nil {
		return "", false, false, err
	}
	direction, failure := validateReference(*tx, ref)
	if failure != "" {
		return reject(failure)
	}
	if _, err = r.Reversals.Get(ctx, ref.ID()); err == nil {
		return reject(FailureAlreadyReversed)
	} else if !errors.Is(err, ports.ErrNotFound) {
		return "", false, false, err
	}
	original := *w
	if direction == ports.Credit {
		err = w.Credit(ref.Amount())
	} else {
		err = w.Debit(ref.Amount())
	}
	if errors.Is(err, wallet.ErrInsufficientFunds) {
		return reject(FailureInsufficientFundsForReversal)
	}
	if errors.Is(err, money.ErrOverflow) || errors.Is(err, wallet.ErrVersionOverflow) {
		return reject(FailureOverflow)
	}
	if err != nil {
		return "", false, false, err
	}
	claimed, err := r.Reversals.Claim(ctx, ports.Reversal{OriginalTransactionID: ref.ID(), ReversalTransactionID: tx.ID(), CreatedAt: at})
	if err != nil {
		return "", false, false, err
	}
	if !claimed {
		*w = original
		return reject(FailureAlreadyReversed)
	}
	return direction, true, false, tx.MarkProcessed(at)
}

func (s *Service) startReversal(ctx context.Context, r ports.Repositories, tx wager.Transaction, w wallet.Wallet, correlation string, at time.Time) (WagerResult, error) {
	before, version := w.Balance(), w.Version()
	direction, changed, missing, err := s.applyReversal(ctx, r, &tx, &w, at)
	if err != nil {
		return WagerResult{}, err
	}
	if missing {
		if err = tx.MarkPendingReference(at); err != nil {
			return WagerResult{}, err
		}
		policy := s.pendingPolicy
		expires := at.Add(policy.TTL)
		next := at.Add(policy.BaseDelay)
		if next.After(expires) {
			next = expires
		}
		if err = r.PendingReferences.Create(ctx, ports.PendingReference{TransactionID: tx.ID(), CorrelationID: correlation, MaxAttempts: policy.MaxAttempts, FirstPendingAt: at, NextAttemptAt: next, ExpiresAt: expires}); err != nil {
			return WagerResult{}, err
		}
	}
	return finishReversal(ctx, r, tx, w, before, version, direction, changed, correlation, wager.StatePending)
}

func finishReversal(ctx context.Context, r ports.Repositories, tx wager.Transaction, w wallet.Wallet, before money.Money, version int64, direction ports.LedgerDirection, changed bool, correlation string, expected wager.State) (WagerResult, error) {
	if changed {
		if err := r.Wallets.Update(ctx, w, version); err != nil {
			return WagerResult{}, err
		}
		entry := ports.LedgerEntry{ID: string(tx.ID()) + ":ledger", WalletID: w.ID(), TransactionID: tx.ID(), Direction: direction, AmountCents: tx.Amount().Cents(), BalanceBeforeCents: before.Cents(), BalanceAfterCents: w.Balance().Cents(), WalletVersion: w.Version(), CreatedAt: tx.UpdatedAt()}
		if err := r.Ledger.Append(ctx, entry); err != nil {
			return WagerResult{}, err
		}
	}
	if err := r.Wagers.Update(ctx, tx, expected); err != nil {
		return WagerResult{}, err
	}
	if err := writeEvents(ctx, r, tx, w, before, direction, correlation, changed); err != nil {
		return WagerResult{}, err
	}
	return reversalResult(tx, w), nil
}
func reversalResult(tx wager.Transaction, w wallet.Wallet) WagerResult {
	return WagerResult{TransactionID: tx.ID(), ProviderID: tx.ProviderID(), ExternalTransactionID: tx.ExternalTransactionID(), WalletID: w.ID(), Type: tx.Type(), State: tx.State(), FailureCode: tx.FailureCode(), Amount: tx.Amount().External(), ObservedBalance: w.Balance().External(), WalletVersion: w.Version(), ProcessedAt: tx.UpdatedAt(), ReferenceExternalTransactionID: tx.ReferenceExternalTransactionID()}
}
