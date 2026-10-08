package wager

import "errors"

var (
	ErrInvalidTransactionID = errors.New("invalid transaction id")
	ErrInvalidProviderID    = errors.New("invalid provider id")
	ErrInvalidExternalID    = errors.New("invalid external transaction id")
	ErrInvalidPlayerID      = errors.New("invalid player id")
	ErrInvalidWalletID      = errors.New("invalid wallet id")
	ErrInvalidRoundID       = errors.New("invalid round id")
	ErrInvalidGameID        = errors.New("invalid game id")
	ErrInvalidType          = errors.New("invalid transaction type")
	ErrInvalidState         = errors.New("invalid transaction state")
	ErrInvalidAmount        = errors.New("invalid amount")
	ErrInvalidCurrency      = errors.New("invalid currency")
	ErrCurrencyMismatch     = errors.New("currency mismatch")
	ErrOpeningNotExternal   = errors.New("opening is not allowed as an external operation")
	ErrInvalidTransition    = errors.New("invalid state transition")
	ErrTerminalState        = errors.New("terminal state cannot transition")
)
