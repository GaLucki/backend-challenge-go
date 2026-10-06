// Package financial implements transport-independent financial use cases.
package financial

import (
	"errors"
	"time"

	"github.com/junglegaming/backend-challenge-go/internal/domain/money"
	"github.com/junglegaming/backend-challenge-go/internal/domain/wager"
	"github.com/junglegaming/backend-challenge-go/internal/domain/wallet"
)

var (
	ErrWalletNotFound               = errors.New("wallet not found")
	ErrWalletExists                 = errors.New("wallet already exists")
	ErrPlayerMismatch               = errors.New("wallet player mismatch")
	ErrCurrencyMismatch             = errors.New("wallet currency mismatch")
	ErrInvalidAmount                = errors.New("invalid amount")
	ErrInsufficientFunds            = errors.New("insufficient funds")
	ErrDuplicateExternalTransaction = errors.New("duplicate external transaction")
	ErrIdempotencyConflict          = errors.New("idempotency conflict")
	ErrIdempotencyKeyRequired       = errors.New("idempotency key required")
	ErrOverflow                     = errors.New("financial overflow")
	ErrInvalidOperationType         = errors.New("invalid operation type")
	ErrInvalidInput                 = errors.New("invalid financial input")
	ErrPersistence                  = errors.New("financial persistence failure")
	ErrReferenceInvalid             = errors.New("invalid reversal reference")
	ErrReferenceNotFound            = errors.New("reference not found")
	ErrAlreadyReversed              = errors.New("transaction already reversed")
	ErrInsufficientFundsForReversal = errors.New("insufficient funds for reversal")
)

const (
	FailureInsufficientFunds            wager.FailureCode = "INSUFFICIENT_FUNDS"
	FailureOverflow                     wager.FailureCode = "OVERFLOW"
	FailureReferenceNotFound            wager.FailureCode = "REFERENCE_NOT_FOUND"
	FailureReferenceTypeInvalid         wager.FailureCode = "REFERENCE_TYPE_INVALID"
	FailureReferenceStateInvalid        wager.FailureCode = "REFERENCE_STATE_INVALID"
	FailureReferenceProviderMismatch    wager.FailureCode = "REFERENCE_PROVIDER_MISMATCH"
	FailureReferencePlayerMismatch      wager.FailureCode = "REFERENCE_PLAYER_MISMATCH"
	FailureReferenceWalletMismatch      wager.FailureCode = "REFERENCE_WALLET_MISMATCH"
	FailureReferenceCurrencyMismatch    wager.FailureCode = "REFERENCE_CURRENCY_MISMATCH"
	FailureReferenceRoundMismatch       wager.FailureCode = "REFERENCE_ROUND_MISMATCH"
	FailureReferenceAmountMismatch      wager.FailureCode = "REFERENCE_AMOUNT_MISMATCH"
	FailureAlreadyReversed              wager.FailureCode = "ALREADY_REVERSED"
	FailureInsufficientFundsForReversal wager.FailureCode = "INSUFFICIENT_FUNDS_FOR_REVERSAL"
)

type CreateWalletInput struct {
	PlayerID       wallet.PlayerID
	Currency       string
	InitialBalance money.Money
	CorrelationID  string
}
type CreateWalletResult struct {
	WalletID             wallet.ID           `json:"walletId"`
	PlayerID             wallet.PlayerID     `json:"playerId"`
	Balance              money.External      `json:"balance"`
	WalletVersion        int64               `json:"walletVersion"`
	OpeningTransactionID wager.TransactionID `json:"openingTransactionId,omitempty"`
}

type WagerInput struct {
	ProviderID                     wager.ProviderID
	ExternalTransactionID          wager.ExternalTransactionID
	PlayerID                       wager.PlayerID
	WalletID                       wallet.ID
	Type                           wager.Type
	Amount                         money.Money
	RoundID                        wager.RoundID
	CorrelationID                  string
	ReferenceExternalTransactionID wager.ExternalTransactionID
}
type HTTPWagerInput struct {
	WagerInput
	IdempotencyKey string
}
type WagerResult struct {
	TransactionID                  wager.TransactionID         `json:"transactionId"`
	ProviderID                     wager.ProviderID            `json:"providerId"`
	ExternalTransactionID          wager.ExternalTransactionID `json:"externalTransactionId"`
	WalletID                       wallet.ID                   `json:"walletId"`
	Type                           wager.Type                  `json:"type"`
	State                          wager.State                 `json:"state"`
	FailureCode                    wager.FailureCode           `json:"failureCode,omitempty"`
	Amount                         money.External              `json:"money"`
	ObservedBalance                money.External              `json:"observedBalance"`
	WalletVersion                  int64                       `json:"walletVersion"`
	ProcessedAt                    time.Time                   `json:"processedAt"`
	IdempotentReplay               bool                        `json:"idempotentReplay"`
	ReferenceExternalTransactionID wager.ExternalTransactionID `json:"referenceExternalTransactionId,omitempty"`
}

// BusinessError classifies committed rejections. A rejection is a result, not
// a transaction error: returning it from the UoW callback would roll it back.
func (r WagerResult) BusinessError() error {
	switch r.FailureCode {
	case FailureInsufficientFunds:
		return ErrInsufficientFunds
	case FailureOverflow:
		return ErrOverflow
	case FailureReferenceNotFound:
		return ErrReferenceNotFound
	case FailureAlreadyReversed:
		return ErrAlreadyReversed
	case FailureInsufficientFundsForReversal:
		return ErrInsufficientFundsForReversal
	case FailureReferenceTypeInvalid, FailureReferenceStateInvalid, FailureReferenceProviderMismatch, FailureReferencePlayerMismatch, FailureReferenceWalletMismatch, FailureReferenceCurrencyMismatch, FailureReferenceRoundMismatch, FailureReferenceAmountMismatch:
		return ErrReferenceInvalid
	default:
		return nil
	}
}
