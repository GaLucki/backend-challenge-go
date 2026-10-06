package wallet

import "errors"

var (
	ErrInvalidID               = errors.New("invalid wallet id")
	ErrInvalidPlayerID         = errors.New("invalid player id")
	ErrInvalidCurrency         = errors.New("invalid wallet currency")
	ErrInvalidVersion          = errors.New("invalid wallet version")
	ErrNegativeBalance         = errors.New("wallet balance cannot be negative")
	ErrCurrencyMismatch        = errors.New("currency mismatch")
	ErrNonPositiveAmount       = errors.New("amount must be positive")
	ErrInsufficientFunds       = errors.New("insufficient funds")
	ErrBalanceCurrencyMismatch = errors.New("balance currency must match wallet currency")
)
