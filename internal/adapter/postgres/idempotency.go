package postgres

import (
	"context"
	"encoding/json"
	"time"

	"github.com/junglegaming/backend-challenge-go/internal/domain/wager"
	"github.com/junglegaming/backend-challenge-go/internal/ports"
)

func (r *idempotencyRepository) Claim(ctx context.Context, key, hash string, at time.Time) (bool, error) {
	if !r.transactional {
		return false, ports.ErrTransactionRequired
	}
	tag, err := r.db.Exec(ctx, `INSERT INTO wager_idempotency_records(idempotency_key,payload_hash,created_at) VALUES($1,$2,$3) ON CONFLICT(idempotency_key) DO NOTHING`, key, hash, at)
	return tag.RowsAffected() == 1, mapError(err)
}
func (r *idempotencyRepository) Get(ctx context.Context, key string) (ports.IdempotencyRecord, error) {
	var record ports.IdempotencyRecord
	var id string
	var result []byte
	err := r.db.QueryRow(ctx, `SELECT idempotency_key,payload_hash,coalesce(transaction_id,''),result,created_at,completed_at FROM wager_idempotency_records WHERE idempotency_key=$1`, key).Scan(&record.Key, &record.PayloadHash, &id, &result, &record.CreatedAt, &record.CompletedAt)
	record.TransactionID = wager.TransactionID(id)
	record.Result = result
	return record, mapError(err)
}
func (r *idempotencyRepository) Complete(ctx context.Context, record ports.IdempotencyRecord) error {
	if !r.transactional {
		return ports.ErrTransactionRequired
	}
	return affectedExec(ctx, r.db, `UPDATE wager_idempotency_records SET transaction_id=$2,result=$3,completed_at=$4 WHERE idempotency_key=$1 AND payload_hash=$5 AND result IS NULL`, ports.ErrConflict, record.Key, string(record.TransactionID), []byte(record.Result), record.CompletedAt, record.PayloadHash)
}

func (r *idempotencyRepository) UpdatePendingResult(ctx context.Context, id wager.TransactionID, result json.RawMessage, at time.Time) error {
	if !r.transactional {
		return ports.ErrTransactionRequired
	}
	_, err := r.db.Exec(ctx, `UPDATE wager_idempotency_records SET result=$2,completed_at=$3 WHERE transaction_id=$1 AND result->>'state'='PENDING_REFERENCE'`, string(id), result, at)
	return mapError(err)
}
