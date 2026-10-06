package money

import "errors"

var (
	ErrInvalidCurrency  = errors.New("invalid currency")
	ErrCurrencyMismatch = errors.New("currency mismatch")
	ErrOverflow         = errors.New("money overflow")
	ErrUnderflow        = errors.New("money underflow")
	ErrInvalidAmount    = errors.New("invalid amount")
	ErrNegativeAmount   = errors.New("negative amount not allowed")
)
