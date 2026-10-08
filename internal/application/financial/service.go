package financial

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/junglegaming/backend-challenge-go/internal/domain/money"
	"github.com/junglegaming/backend-challenge-go/internal/domain/wager"
	"github.com/junglegaming/backend-challenge-go/internal/domain/wallet"
	"github.com/junglegaming/backend-challenge-go/internal/ports"
)

type Service struct {
	metrics       ports.Telemetry
	logger        *slog.Logger
	uow           ports.UnitOfWork
	now           func() time.Time
	newID         func() (string, error)
	pendingPolicy PendingPolicy
}

func NewService(uow ports.UnitOfWork) *Service {
	return &Service{uow: uow, now: func() time.Time { return time.Now().UTC().Truncate(time.Microsecond) }, newID: randomID, pendingPolicy: DefaultPendingPolicy()}
}
func randomID() (string, error) {
	var bytes [16]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(bytes[:]), nil
}

func (s *Service) CreateWallet(ctx context.Context, in CreateWalletInput) (result CreateWalletResult, resultErr error) {
	started := time.Now()
	defer func() {
		if in.InitialBalance.IsPositive() {
			s.observeFinancial(ctx, WagerInput{Type: wager.TypeOpening, WalletID: result.WalletID, CorrelationID: in.CorrelationID}, WagerResult{TransactionID: result.OpeningTransactionID, State: wager.StateProcessed}, resultErr, started, "new")
		}
	}()
	if in.InitialBalance.Currency() == "" || in.InitialBalance.IsNegative() {
		return CreateWalletResult{}, ErrInvalidAmount
	}
	if _, err := money.New(0, in.Currency); err != nil {
		return CreateWalletResult{}, ErrCurrencyMismatch
	}
	if in.Currency != in.InitialBalance.Currency() {
		return CreateWalletResult{}, ErrCurrencyMismatch
	}
	id, err := s.newID()
	if err != nil {
		return CreateWalletResult{}, ErrPersistence
	}
	w, err := wallet.New(wallet.ID(id), in.PlayerID, in.Currency, in.InitialBalance)
	if err != nil {
		return CreateWalletResult{}, ErrInvalidInput
	}
	result = CreateWalletResult{WalletID: w.ID(), PlayerID: w.PlayerID(), Balance: w.Balance().External(), WalletVersion: w.Version()}
	err = s.uow.WithinTransaction(ctx, func(r ports.Repositories) error {
		if err := r.Wallets.Create(ctx, w); err != nil {
			if errors.Is(err, ports.ErrUniqueViolation) {
				return ErrWalletExists
			}
			return err
		}
		if in.InitialBalance.IsZero() {
			return nil
		}
		txID, err := s.newID()
		if err != nil {
			return ErrPersistence
		}
		at := s.now()
		tx, err := wager.NewOpening(wager.OpeningParams{ID: wager.TransactionID(txID), PlayerID: wager.PlayerID(w.PlayerID()), WalletID: wager.WalletID(w.ID()), Amount: w.Balance(), Now: at})
		if err != nil {
			return ErrInvalidInput
		}
		if err = r.Wagers.Create(ctx, tx); err != nil {
			return err
		}
		entry := ports.LedgerEntry{ID: txID + ":ledger", WalletID: w.ID(), TransactionID: tx.ID(), Direction: ports.Credit, AmountCents: w.Balance().Cents(), BalanceBeforeCents: 0, BalanceAfterCents: w.Balance().Cents(), WalletVersion: w.Version(), CreatedAt: at}
		if err = r.Ledger.Append(ctx, entry); err != nil {
			return err
		}
		before, _ := money.Zero(w.Currency())
		if err = writeEvents(ctx, r, tx, w, before, ports.Credit, in.CorrelationID, true); err != nil {
			return err
		}
		result.OpeningTransactionID = tx.ID()
		return nil
	})
	if err != nil {
		return CreateWalletResult{}, applicationError(err)
	}
	return result, nil
}

func validateWager(in WagerInput) error {
	switch in.Type {
	case wager.TypeBet, wager.TypeWin:
		if !in.Amount.IsPositive() {
			return ErrInvalidAmount
		}
	case wager.TypeRefund, wager.TypeRollback:
		if !in.Amount.IsPositive() {
			return ErrInvalidAmount
		}
	case wager.TypeLoss:
		if !in.Amount.IsZero() {
			return ErrInvalidAmount
		}
	default:
		return ErrInvalidOperationType
	}
	if isReversal(in.Type) {
		if strings.TrimSpace(string(in.ReferenceExternalTransactionID)) == "" {
			return ErrInvalidInput
		}
	} else if in.Type != wager.TypeWin && in.ReferenceExternalTransactionID != "" {
		return ErrInvalidInput
	}
	if in.Amount.Currency() == "" {
		return ErrCurrencyMismatch
	}
	for _, id := range []string{string(in.ProviderID), string(in.ExternalTransactionID), string(in.PlayerID), string(in.WalletID), string(in.RoundID)} {
		if strings.TrimSpace(id) == "" {
			return ErrInvalidInput
		}
	}
	return nil
}

// ProcessWager is the transport-independent financial entry point. External
// identity uniqueness still applies; future delivery adapters can reuse the core.
func (s *Service) ProcessWager(ctx context.Context, in WagerInput) (result WagerResult, resultErr error) {
	started := time.Now()
	defer func() { s.observeFinancial(ctx, in, result, resultErr, started, "new") }()
	if err := validateWager(in); err != nil {
		return WagerResult{}, err
	}
	err := s.uow.WithinTransaction(ctx, func(r ports.Repositories) error {
		var err error
		result, err = s.process(ctx, r, in)
		return err
	})
	if err != nil {
		return WagerResult{}, applicationError(err)
	}
	return result, nil
}

// ProcessHTTPWager adds durable idempotency without importing an HTTP transport.
func (s *Service) ProcessHTTPWager(ctx context.Context, in HTTPWagerInput) (result WagerResult, resultErr error) {
	started := time.Now()
	defer func() {
		outcome := "new"
		if result.IdempotentReplay {
			outcome = "replay"
		}
		s.observeFinancial(ctx, in.WagerInput, result, resultErr, started, outcome)
	}()
	if strings.TrimSpace(in.IdempotencyKey) == "" {
		return WagerResult{}, ErrIdempotencyKeyRequired
	}
	err := s.uow.WithinTransaction(ctx, func(r ports.Repositories) error {
		var err error
		result, err = s.processIdempotent(ctx, r, in)
		return err
	})
	if err != nil {
		return WagerResult{}, applicationError(err)
	}
	return result, nil
}

// Reused inside the caller's UoW, including Inbox. It never begins a nested
// transaction, and neither transport observes success until that UoW commits.
func (s *Service) processIdempotent(ctx context.Context, r ports.Repositories, in HTTPWagerInput) (WagerResult, error) {
	hash := CanonicalPayloadHash(in.WagerInput)
	claimed, err := r.Idempotency.Claim(ctx, in.IdempotencyKey, hash, s.now())
	if err != nil {
		return WagerResult{}, err
	}
	var result WagerResult
	if !claimed {
		record, err := r.Idempotency.Get(ctx, in.IdempotencyKey)
		if err != nil {
			return result, err
		}
		if record.PayloadHash != hash {
			return result, ErrIdempotencyConflict
		}
		if record.CompletedAt == nil || len(record.Result) == 0 {
			return result, ErrPersistence
		}
		if json.Unmarshal(record.Result, &result) != nil {
			return WagerResult{}, ErrPersistence
		}
		result.IdempotentReplay = true
		return result, nil
	}
	// An existing key takes precedence over changed typed input validation.
	if err := validateWager(in.WagerInput); err != nil {
		return result, err
	}
	result, err = s.process(ctx, r, in.WagerInput)
	if err != nil {
		return WagerResult{}, err
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		return WagerResult{}, ErrPersistence
	}
	completed := result.ProcessedAt
	err = r.Idempotency.Complete(ctx, ports.IdempotencyRecord{Key: in.IdempotencyKey, PayloadHash: hash, TransactionID: result.TransactionID, Result: encoded, CompletedAt: &completed})
	return result, err
}

func (s *Service) process(ctx context.Context, r ports.Repositories, in WagerInput) (WagerResult, error) {
	w, err := r.Wallets.GetForUpdate(ctx, in.WalletID)
	if errors.Is(err, ports.ErrNotFound) {
		return WagerResult{}, ErrWalletNotFound
	}
	if err != nil {
		return WagerResult{}, err
	}
	if string(w.PlayerID()) != string(in.PlayerID) {
		return WagerResult{}, ErrPlayerMismatch
	}
	if w.Currency() != in.Amount.Currency() {
		return WagerResult{}, ErrCurrencyMismatch
	}
	if _, err = r.Wagers.GetByExternalID(ctx, in.ProviderID, in.ExternalTransactionID); err == nil {
		return WagerResult{}, ErrDuplicateExternalTransaction
	} else if !errors.Is(err, ports.ErrNotFound) {
		return WagerResult{}, err
	}
	id, err := s.newID()
	if err != nil {
		return WagerResult{}, ErrPersistence
	}
	at := s.now()
	tx, err := wager.NewExternal(wager.ExternalParams{ID: wager.TransactionID(id), ProviderID: in.ProviderID, ExternalTransactionID: in.ExternalTransactionID, PlayerID: in.PlayerID, WalletID: wager.WalletID(in.WalletID), Type: in.Type, Amount: in.Amount, RoundID: in.RoundID, GameID: in.GameID, Now: at, ReferenceExternalTransactionID: in.ReferenceExternalTransactionID})
	if err != nil {
		return WagerResult{}, ErrInvalidInput
	}
	if isReversal(in.Type) {
		if err := r.Wagers.Create(ctx, tx); err != nil {
			if errors.Is(err, ports.ErrUniqueViolation) {
				return WagerResult{}, ErrDuplicateExternalTransaction
			}
			return WagerResult{}, err
		}
		return s.startReversal(ctx, r, tx, w, in.CorrelationID, at)
	}
	before, version := w.Balance(), w.Version()
	var operationErr error
	var direction ports.LedgerDirection
	failure := wager.FailureCode("")
	if in.Type == wager.TypeWin && in.ReferenceExternalTransactionID != "" {
		failure, err = validateWinReference(ctx, r, tx)
		if err != nil {
			return WagerResult{}, err
		}
	}
	if failure == "" {
		switch in.Type {
		case wager.TypeBet:
			direction = ports.Debit
			operationErr = w.Debit(in.Amount)
		case wager.TypeWin:
			direction = ports.Credit
			operationErr = w.Credit(in.Amount)
		}
	}
	if errors.Is(operationErr, wallet.ErrInsufficientFunds) {
		failure = FailureInsufficientFunds
	} else if errors.Is(operationErr, money.ErrOverflow) || errors.Is(operationErr, wallet.ErrVersionOverflow) {
		failure = FailureOverflow
	} else if operationErr != nil {
		return WagerResult{}, ErrInvalidInput
	}
	if failure != "" {
		err = tx.MarkRejected(failure, at)
	} else {
		err = tx.MarkProcessed(at)
	}
	if err != nil {
		return WagerResult{}, ErrInvalidInput
	}
	if err = r.Wagers.Create(ctx, tx); err != nil {
		if errors.Is(err, ports.ErrUniqueViolation) {
			return WagerResult{}, ErrDuplicateExternalTransaction
		}
		return WagerResult{}, err
	}
	changed := failure == "" && in.Type != wager.TypeLoss
	if changed {
		if err = r.Wallets.Update(ctx, w, version); err != nil {
			return WagerResult{}, err
		}
		entry := ports.LedgerEntry{ID: id + ":ledger", WalletID: w.ID(), TransactionID: tx.ID(), Direction: direction, AmountCents: in.Amount.Cents(), BalanceBeforeCents: before.Cents(), BalanceAfterCents: w.Balance().Cents(), WalletVersion: w.Version(), CreatedAt: at}
		if err = r.Ledger.Append(ctx, entry); err != nil {
			return WagerResult{}, err
		}
	}
	if err = writeEvents(ctx, r, tx, w, before, direction, in.CorrelationID, changed); err != nil {
		return WagerResult{}, err
	}
	return WagerResult{TransactionID: tx.ID(), ProviderID: tx.ProviderID(), ExternalTransactionID: tx.ExternalTransactionID(), WalletID: w.ID(), Type: tx.Type(), State: tx.State(), FailureCode: tx.FailureCode(), Amount: tx.Amount().External(), ObservedBalance: w.Balance().External(), WalletVersion: w.Version(), ProcessedAt: at}, nil
}

func applicationError(err error) error {
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	for _, stable := range []error{ErrWalletNotFound, ErrWalletExists, ErrPlayerMismatch, ErrCurrencyMismatch, ErrInvalidAmount, ErrInsufficientFunds, ErrDuplicateExternalTransaction, ErrIdempotencyConflict, ErrIdempotencyKeyRequired, ErrOverflow, ErrInvalidOperationType, ErrInvalidInput, ErrPersistence, ErrReferenceInvalid, ErrReferenceNotFound, ErrAlreadyReversed, ErrInsufficientFundsForReversal} {
		if errors.Is(err, stable) {
			return stable
		}
	}
	return ErrPersistence
}
