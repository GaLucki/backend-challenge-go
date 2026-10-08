package ports

import (
	"context"

	"github.com/junglegaming/backend-challenge-go/internal/domain/wager"
	"github.com/junglegaming/backend-challenge-go/internal/domain/wallet"
)

// AuditTransaction preserves persisted evidence, including rows that cannot be
// rehydrated as valid domain entities. Currency on a ledger entry is implicit in
// its wallet; the transaction carries the currency to compare against it.
type AuditTransaction struct {
	ID                  wager.TransactionID
	ProviderID          wager.ProviderID
	ExternalID          wager.ExternalTransactionID
	PlayerID            wager.PlayerID
	WalletID            wager.WalletID
	Currency            string
	Type                wager.Type
	AmountCents         int64
	RoundID             wager.RoundID
	ReferenceExternalID wager.ExternalTransactionID
	State               wager.State
}

type ReconciliationSnapshot struct {
	Wallet wallet.Wallet
	Ledger []LedgerEntry
	// Includes wallet transactions, ledger-linked transactions and immediate
	// reversal references, deduplicated, in deterministic ID order.
	Transactions []AuditTransaction
}

// ReconciliationReader must return the complete history in one consistent,
// read-only snapshot, or an error. It must never return partial history.
type ReconciliationReader interface {
	ReadReconciliation(context.Context, wallet.ID) (ReconciliationSnapshot, error)
}
