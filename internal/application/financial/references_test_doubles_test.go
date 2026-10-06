package financial

import (
	"context"
	"encoding/json"
	"time"

	"github.com/junglegaming/backend-challenge-go/internal/domain/wager"
	"github.com/junglegaming/backend-challenge-go/internal/ports"
)

func (r keyDouble) UpdatePendingResult(_ context.Context, id wager.TransactionID, result json.RawMessage, at time.Time) error {
	if r.fail {
		return ports.ErrPersistence
	}
	for key, record := range r.state.keys {
		if record.TransactionID == id {
			record.Result = result
			record.CompletedAt = &at
			r.state.keys[key] = record
		}
	}
	return nil
}

type reversalDouble struct{ state *memoryState }

func (r reversalDouble) Get(_ context.Context, id wager.TransactionID) (ports.Reversal, error) {
	rev, ok := r.state.reversals[id]
	if !ok {
		return rev, ports.ErrNotFound
	}
	return rev, nil
}
func (r reversalDouble) Claim(_ context.Context, rev ports.Reversal) (bool, error) {
	if _, ok := r.state.reversals[rev.OriginalTransactionID]; ok {
		return false, nil
	}
	r.state.reversals[rev.OriginalTransactionID] = rev
	return true, nil
}

type pendingDouble struct{ state *memoryState }

func (r pendingDouble) Create(_ context.Context, p ports.PendingReference) error {
	r.state.pending[p.TransactionID] = p
	return nil
}
func (r pendingDouble) ClaimNext(_ context.Context, at time.Time) (ports.PendingReference, error) {
	var chosen ports.PendingReference
	found := false
	for _, p := range r.state.pending {
		if p.CompletedAt == nil && !p.NextAttemptAt.After(at) && (!found || p.NextAttemptAt.Before(chosen.NextAttemptAt) || p.NextAttemptAt.Equal(chosen.NextAttemptAt) && p.TransactionID < chosen.TransactionID) {
			chosen = p
			found = true
		}
	}
	if !found {
		return chosen, ports.ErrNotFound
	}
	return chosen, nil
}
func (r pendingDouble) Update(_ context.Context, p ports.PendingReference) error {
	r.state.pending[p.TransactionID] = p
	return nil
}
