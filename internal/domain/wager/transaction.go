package wager

import (
	"strings"
	"time"

	"github.com/junglegaming/backend-challenge-go/internal/domain/money"
)

// TransactionID uniquely identifies a wager transaction.
type TransactionID string

// ProviderID identifies an external game provider.
type ProviderID string

// ExternalTransactionID is the provider-side transaction identifier.
type ExternalTransactionID string

// PlayerID identifies a player.
type PlayerID string

// WalletID identifies a wallet.
type WalletID string

// RoundID identifies a game round.
type RoundID string

// FailureCode is a stable domain failure identifier.
type FailureCode string

// Transaction is the wager transaction entity/aggregate.
type Transaction struct {
	id                             TransactionID
	providerID                     ProviderID
	externalTransactionID          ExternalTransactionID
	playerID                       PlayerID
	walletID                       WalletID
	currency                       string
	txType                         Type
	amount                         money.Money
	roundID                        RoundID
	referenceExternalTransactionID ExternalTransactionID
	state                          State
	failureCode                    FailureCode
	createdAt                      time.Time
	updatedAt                      time.Time
}

// ExternalParams carries data required to create an external wager transaction.
type ExternalParams struct {
	ID                             TransactionID
	ProviderID                     ProviderID
	ExternalTransactionID          ExternalTransactionID
	PlayerID                       PlayerID
	WalletID                       WalletID
	RoundID                        RoundID
	Type                           Type
	Amount                         money.Money
	ReferenceExternalTransactionID ExternalTransactionID
	Now                            time.Time
}

// OpeningParams carries data required to create an internal OPENING transaction.
type OpeningParams struct {
	ID       TransactionID
	PlayerID PlayerID
	WalletID WalletID
	Amount   money.Money
	Now      time.Time
}

// NewExternal creates a PENDING external wager transaction.
// OPENING is rejected.
func NewExternal(p ExternalParams) (Transaction, error) {
	if p.Type == TypeOpening {
		return Transaction{}, ErrOpeningNotExternal
	}
	if !p.Type.IsExternal() {
		return Transaction{}, ErrInvalidType
	}
	if strings.TrimSpace(string(p.ProviderID)) == "" {
		return Transaction{}, ErrInvalidProviderID
	}
	if strings.TrimSpace(string(p.ExternalTransactionID)) == "" {
		return Transaction{}, ErrInvalidExternalID
	}
	if strings.TrimSpace(string(p.RoundID)) == "" {
		return Transaction{}, ErrInvalidRoundID
	}

	return newTransaction(
		p.ID,
		p.ProviderID,
		p.ExternalTransactionID,
		p.PlayerID,
		p.WalletID,
		p.RoundID,
		p.Type,
		p.Amount,
		p.ReferenceExternalTransactionID,
		StatePending,
		p.Now,
	)
}

// NewOpening creates an internal OPENING transaction in PROCESSED state.
func NewOpening(p OpeningParams) (Transaction, error) {
	return newTransaction(
		p.ID,
		"",
		"",
		p.PlayerID,
		p.WalletID,
		"",
		TypeOpening,
		p.Amount,
		"",
		StateProcessed,
		p.Now,
	)
}

func newTransaction(
	id TransactionID,
	providerID ProviderID,
	externalID ExternalTransactionID,
	playerID PlayerID,
	walletID WalletID,
	roundID RoundID,
	txType Type,
	amount money.Money,
	reference ExternalTransactionID,
	state State,
	now time.Time,
) (Transaction, error) {
	if strings.TrimSpace(string(id)) == "" {
		return Transaction{}, ErrInvalidTransactionID
	}
	if strings.TrimSpace(string(playerID)) == "" {
		return Transaction{}, ErrInvalidPlayerID
	}
	if strings.TrimSpace(string(walletID)) == "" {
		return Transaction{}, ErrInvalidWalletID
	}
	if !txType.valid() {
		return Transaction{}, ErrInvalidType
	}
	if !state.valid() {
		return Transaction{}, ErrInvalidState
	}
	if amount.Currency() == "" {
		return Transaction{}, ErrInvalidCurrency
	}
	if amount.IsNegative() {
		return Transaction{}, ErrInvalidAmount
	}

	if now.IsZero() {
		now = time.Now().UTC()
	} else {
		now = now.UTC()
	}

	return Transaction{
		id:                             id,
		providerID:                     providerID,
		externalTransactionID:          externalID,
		playerID:                       playerID,
		walletID:                       walletID,
		currency:                       amount.Currency(),
		txType:                         txType,
		amount:                         amount,
		roundID:                        roundID,
		referenceExternalTransactionID: reference,
		state:                          state,
		createdAt:                      now,
		updatedAt:                      now,
	}, nil
}

// Accessors

func (t Transaction) ID() TransactionID      { return t.id }
func (t Transaction) ProviderID() ProviderID { return t.providerID }
func (t Transaction) ExternalTransactionID() ExternalTransactionID {
	return t.externalTransactionID
}
func (t Transaction) PlayerID() PlayerID  { return t.playerID }
func (t Transaction) WalletID() WalletID  { return t.walletID }
func (t Transaction) Currency() string    { return t.currency }
func (t Transaction) Type() Type          { return t.txType }
func (t Transaction) Amount() money.Money { return t.amount }
func (t Transaction) RoundID() RoundID    { return t.roundID }
func (t Transaction) ReferenceExternalTransactionID() ExternalTransactionID {
	return t.referenceExternalTransactionID
}
func (t Transaction) State() State             { return t.state }
func (t Transaction) FailureCode() FailureCode { return t.failureCode }
func (t Transaction) CreatedAt() time.Time     { return t.createdAt }
func (t Transaction) UpdatedAt() time.Time     { return t.updatedAt }

// MarkProcessed transitions the transaction to PROCESSED.
func (t *Transaction) MarkProcessed(now time.Time) error {
	return t.transition(StateProcessed, "", now)
}

// MarkRejected transitions the transaction to REJECTED with a failure code.
func (t *Transaction) MarkRejected(code FailureCode, now time.Time) error {
	return t.transition(StateRejected, code, now)
}

// MarkFailed transitions the transaction to FAILED with a failure code.
func (t *Transaction) MarkFailed(code FailureCode, now time.Time) error {
	return t.transition(StateFailed, code, now)
}

// MarkPendingReference transitions the transaction to PENDING_REFERENCE.
func (t *Transaction) MarkPendingReference(now time.Time) error {
	return t.transition(StatePendingReference, "", now)
}

func (t *Transaction) transition(to State, code FailureCode, now time.Time) error {
	if t == nil {
		return ErrInvalidTransactionID
	}
	if t.state.IsTerminal() {
		return ErrTerminalState
	}
	if !canTransition(t.state, to) {
		return ErrInvalidTransition
	}
	if now.IsZero() {
		now = time.Now().UTC()
	} else {
		now = now.UTC()
	}

	t.state = to
	t.failureCode = code
	t.updatedAt = now
	return nil
}
