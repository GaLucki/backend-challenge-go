// Package ports defines infrastructure-independent persistence contracts.
package ports

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/junglegaming/backend-challenge-go/internal/domain/wager"
	"github.com/junglegaming/backend-challenge-go/internal/domain/wallet"
)

var (
	ErrPersistence         = errors.New("persistence failure")
	ErrNotFound            = errors.New("record not found")
	ErrUniqueViolation     = errors.New("duplicate record")
	ErrConstraintViolation = errors.New("persistence constraint violation")
	ErrTransaction         = errors.New("transaction failure")
	ErrTransactionRequired = errors.New("active transaction required")
	ErrConflict            = errors.New("concurrent modification")
)

type WalletRepository interface {
	Create(context.Context, wallet.Wallet) error
	Get(context.Context, wallet.ID) (wallet.Wallet, error)
	GetForUpdate(context.Context, wallet.ID) (wallet.Wallet, error)
	Update(context.Context, wallet.Wallet, int64) error
}

type WagerTransactionRepository interface {
	Create(context.Context, wager.Transaction) error
	Get(context.Context, wager.TransactionID) (wager.Transaction, error)
	GetByExternalID(context.Context, wager.ProviderID, wager.ExternalTransactionID) (wager.Transaction, error)
	Update(context.Context, wager.Transaction, wager.State) error
}

type LedgerDirection string

const (
	Credit LedgerDirection = "CREDIT"
	Debit  LedgerDirection = "DEBIT"
)

type LedgerEntry struct {
	ID                 string
	WalletID           wallet.ID
	TransactionID      wager.TransactionID
	Direction          LedgerDirection
	AmountCents        int64
	BalanceBeforeCents int64
	BalanceAfterCents  int64
	WalletVersion      int64
	CreatedAt          time.Time
}
type LedgerRepository interface {
	Append(context.Context, LedgerEntry) error
	Get(context.Context, string) (LedgerEntry, error)
}

// PayloadHash is the lowercase hex SHA-256 of the validated canonical envelope.
type InboxMessage struct {
	ConsumerName string
	MessageID    string
	PayloadHash  string
	ReceivedAt   time.Time
	CompletedAt  *time.Time
}
type InboxRepository interface {
	Create(context.Context, InboxMessage) error
	Claim(context.Context, InboxMessage) (bool, error)
	GetForUpdate(context.Context, string, string) (InboxMessage, error)
	Get(context.Context, string, string) (InboxMessage, error)
	Complete(context.Context, string, string, time.Time) error
}

type OutboxEvent struct {
	EventID       string
	AggregateID   string
	EventType     string
	Payload       json.RawMessage
	OccurredAt    time.Time
	RetryCount    int32
	NextAttemptAt time.Time
	PublishedAt   *time.Time
}
type OutboxRepository interface {
	Create(context.Context, OutboxEvent) error
	Get(context.Context, string) (OutboxEvent, error)
}

type IdempotencyRecord struct {
	Key           string
	PayloadHash   string
	TransactionID wager.TransactionID
	Result        json.RawMessage
	CreatedAt     time.Time
	CompletedAt   *time.Time
}

type IdempotencyRepository interface {
	// Claim inserts an incomplete record without aborting on a duplicate key.
	// False means a prior committed record exists; read it in the next statement.
	Claim(context.Context, string, string, time.Time) (bool, error)
	Get(context.Context, string) (IdempotencyRecord, error)
	Complete(context.Context, IdempotencyRecord) error
	// UpdatePendingResult atomically replaces only a saved PENDING_REFERENCE result.
	// A transport-independent operation may have no idempotency record.
	UpdatePendingResult(context.Context, wager.TransactionID, json.RawMessage, time.Time) error
}

type Reversal struct {
	OriginalTransactionID wager.TransactionID
	ReversalTransactionID wager.TransactionID
	CreatedAt             time.Time
}
type ReversalRepository interface {
	Get(context.Context, wager.TransactionID) (Reversal, error)
	// Claim uses a unique original ID; false means it was already reversed.
	Claim(context.Context, Reversal) (bool, error)
}
type PendingReference struct {
	TransactionID  wager.TransactionID
	CorrelationID  string
	AttemptCount   int32
	MaxAttempts    int32
	FirstPendingAt time.Time
	LastAttemptAt  *time.Time
	NextAttemptAt  time.Time
	ExpiresAt      time.Time
	CompletedAt    *time.Time
}
type PendingReferenceRepository interface {
	Create(context.Context, PendingReference) error
	// ClaimNext locks one due item until this UoW completes, skipping locked items.
	ClaimNext(context.Context, time.Time) (PendingReference, error)
	Update(context.Context, PendingReference) error
}

// Repositories passed to the callback are scoped to its single SQL transaction.
// They must not escape the callback or be used concurrently by multiple goroutines.
type Repositories struct {
	Wallets           WalletRepository
	Wagers            WagerTransactionRepository
	Ledger            LedgerRepository
	Inbox             InboxRepository
	Outbox            OutboxRepository
	Idempotency       IdempotencyRepository
	Reversals         ReversalRepository
	PendingReferences PendingReferenceRepository
}
type UnitOfWork interface {
	WithinTransaction(context.Context, func(Repositories) error) error
}
