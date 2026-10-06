package wallet

import (
	"math"
	"strings"

	"github.com/junglegaming/backend-challenge-go/internal/domain/money"
)

// ID uniquely identifies a wallet.
type ID string

// PlayerID identifies the player that owns a wallet.
type PlayerID string

// Wallet is the financial aggregate root.
type Wallet struct {
	id       ID
	playerID PlayerID
	currency string
	balance  money.Money
	version  int64
}

// New creates a wallet with version 1 and a non-negative balance.
func New(id ID, playerID PlayerID, currency string, balance money.Money) (Wallet, error) {
	if strings.TrimSpace(string(id)) == "" {
		return Wallet{}, ErrInvalidID
	}
	if strings.TrimSpace(string(playerID)) == "" {
		return Wallet{}, ErrInvalidPlayerID
	}
	if len(currency) != 3 {
		return Wallet{}, ErrInvalidCurrency
	}
	for i := 0; i < 3; i++ {
		c := currency[i]
		if c < 'A' || c > 'Z' {
			return Wallet{}, ErrInvalidCurrency
		}
	}
	if balance.Currency() != currency {
		return Wallet{}, ErrBalanceCurrencyMismatch
	}
	if balance.IsNegative() {
		return Wallet{}, ErrNegativeBalance
	}

	return Wallet{
		id:       id,
		playerID: playerID,
		currency: currency,
		balance:  balance,
		version:  1,
	}, nil
}

// Rehydrate rebuilds a wallet from persisted state without applying financial operations.
func Rehydrate(id ID, playerID PlayerID, currency string, balance money.Money, version int64) (Wallet, error) {
	w, err := New(id, playerID, currency, balance)
	if err != nil {
		return Wallet{}, err
	}
	if version < 1 {
		return Wallet{}, ErrInvalidVersion
	}
	w.version = version
	return w, nil
}

// ID returns the wallet identifier.
func (w Wallet) ID() ID { return w.id }

// PlayerID returns the owning player identifier.
func (w Wallet) PlayerID() PlayerID { return w.playerID }

// Currency returns the wallet currency.
func (w Wallet) Currency() string { return w.currency }

// Balance returns the current balance.
func (w Wallet) Balance() money.Money { return w.balance }

// Version returns the optimistic concurrency version.
func (w Wallet) Version() int64 { return w.version }

// Credit increases the wallet balance by a positive amount of the same currency.
func (w *Wallet) Credit(amount money.Money) error {
	if w == nil {
		return ErrInvalidID
	}
	if !amount.IsPositive() {
		return ErrNonPositiveAmount
	}
	if amount.Currency() != w.currency {
		return ErrCurrencyMismatch
	}

	next, err := w.balance.Add(amount)
	if err != nil {
		return err
	}

	if w.version == math.MaxInt64 {
		return ErrVersionOverflow
	}
	w.balance = next
	w.version++
	return nil
}

// Debit decreases the wallet balance by a positive amount of the same currency.
// On failure, the wallet state remains unchanged.
func (w *Wallet) Debit(amount money.Money) error {
	if w == nil {
		return ErrInvalidID
	}
	if !amount.IsPositive() {
		return ErrNonPositiveAmount
	}
	if amount.Currency() != w.currency {
		return ErrCurrencyMismatch
	}

	next, err := w.balance.Sub(amount)
	if err != nil {
		return err
	}
	if next.IsNegative() {
		return ErrInsufficientFunds
	}

	if w.version == math.MaxInt64 {
		return ErrVersionOverflow
	}
	w.balance = next
	w.version++
	return nil
}
