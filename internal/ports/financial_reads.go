package ports

import (
	"context"
	"encoding/json"

	"github.com/junglegaming/backend-challenge-go/internal/domain/wager"
	"github.com/junglegaming/backend-challenge-go/internal/domain/wallet"
)

type LedgerPosition struct {
	WalletVersion int64
	EntryID       string
}

// TransactionSnapshot reads transaction and optional saved HTTP result in one
// database statement so pending resolution cannot produce a mixed-state view.
type TransactionSnapshot struct {
	Transaction wager.Transaction
	SavedResult json.RawMessage
}

// FinancialReader is separate from transactional write repositories; no SQL or
// pagination implementation is exposed to HTTP handlers.
type FinancialReader interface {
	ListLedger(context.Context, wallet.ID, LedgerPosition, int) ([]LedgerEntry, error)
	GetTransactionSnapshot(context.Context, wager.TransactionID) (TransactionSnapshot, error)
}
