package wager

import "time"

// PersistedState contains the complete state needed to rebuild a transaction.
type PersistedState struct {
	ExternalParams
	State       State
	FailureCode FailureCode
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// Rehydrate validates identity and monetary invariants without replaying transitions.
func Rehydrate(p PersistedState) (Transaction, error) {
	var t Transaction
	var err error
	if p.Type == TypeOpening {
		t, err = NewOpening(OpeningParams{ID: p.ID, PlayerID: p.PlayerID, WalletID: p.WalletID, Amount: p.Amount, Now: p.CreatedAt})
		if p.ProviderID != "" || p.ExternalTransactionID != "" || p.RoundID != "" || p.ReferenceExternalTransactionID != "" || p.State != StateProcessed {
			return Transaction{}, ErrInvalidState
		}
	} else {
		p.ExternalParams.Now = p.CreatedAt
		t, err = NewExternal(p.ExternalParams)
	}
	if err != nil {
		return Transaction{}, err
	}
	if !p.State.valid() {
		return Transaction{}, ErrInvalidState
	}
	t.state, t.failureCode = p.State, p.FailureCode
	t.createdAt, t.updatedAt = p.CreatedAt.UTC(), p.UpdatedAt.UTC()
	return t, nil
}
