package postgres

import (
	"context"
	"time"

	"github.com/junglegaming/backend-challenge-go/internal/domain/wager"
	"github.com/junglegaming/backend-challenge-go/internal/ports"
)

func (r *reversalRepository) Get(ctx context.Context, id wager.TransactionID) (ports.Reversal, error) {
	var reversal ports.Reversal
	var original, reverse string
	err := r.db.QueryRow(ctx, `SELECT original_transaction_id,reversal_transaction_id,created_at FROM wager_reversals WHERE original_transaction_id=$1`, string(id)).Scan(&original, &reverse, &reversal.CreatedAt)
	reversal.OriginalTransactionID = wager.TransactionID(original)
	reversal.ReversalTransactionID = wager.TransactionID(reverse)
	return reversal, mapError(err)
}
func (r *reversalRepository) Claim(ctx context.Context, rev ports.Reversal) (bool, error) {
	if !r.transactional {
		return false, ports.ErrTransactionRequired
	}
	tag, err := r.db.Exec(ctx, `INSERT INTO wager_reversals(original_transaction_id,reversal_transaction_id,created_at) VALUES($1,$2,$3) ON CONFLICT(original_transaction_id) DO NOTHING`, string(rev.OriginalTransactionID), string(rev.ReversalTransactionID), rev.CreatedAt)
	return tag.RowsAffected() == 1, mapError(err)
}
func (r *pendingReferenceRepository) Create(ctx context.Context, p ports.PendingReference) error {
	if !r.transactional {
		return ports.ErrTransactionRequired
	}
	_, err := r.db.Exec(ctx, `INSERT INTO pending_wager_references(transaction_id,correlation_id,attempt_count,max_attempts,first_pending_at,last_attempt_at,next_attempt_at,expires_at,completed_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`, string(p.TransactionID), p.CorrelationID, p.AttemptCount, p.MaxAttempts, p.FirstPendingAt, p.LastAttemptAt, p.NextAttemptAt, p.ExpiresAt, p.CompletedAt)
	return mapError(err)
}
func (r *pendingReferenceRepository) ClaimNext(ctx context.Context, at time.Time) (ports.PendingReference, error) {
	if !r.transactional {
		return ports.PendingReference{}, ports.ErrTransactionRequired
	}
	var p ports.PendingReference
	var id string
	err := r.db.QueryRow(ctx, `SELECT transaction_id,correlation_id,attempt_count,max_attempts,first_pending_at,last_attempt_at,next_attempt_at,expires_at,completed_at FROM pending_wager_references WHERE completed_at IS NULL AND next_attempt_at <= $1 ORDER BY next_attempt_at,transaction_id LIMIT 1 FOR UPDATE SKIP LOCKED`, at).Scan(&id, &p.CorrelationID, &p.AttemptCount, &p.MaxAttempts, &p.FirstPendingAt, &p.LastAttemptAt, &p.NextAttemptAt, &p.ExpiresAt, &p.CompletedAt)
	p.TransactionID = wager.TransactionID(id)
	return p, mapError(err)
}
func (r *pendingReferenceRepository) Update(ctx context.Context, p ports.PendingReference) error {
	if !r.transactional {
		return ports.ErrTransactionRequired
	}
	return affectedExec(ctx, r.db, `UPDATE pending_wager_references SET attempt_count=$2,last_attempt_at=$3,next_attempt_at=$4,completed_at=$5 WHERE transaction_id=$1 AND completed_at IS NULL`, ports.ErrConflict, string(p.TransactionID), p.AttemptCount, p.LastAttemptAt, p.NextAttemptAt, p.CompletedAt)
}
