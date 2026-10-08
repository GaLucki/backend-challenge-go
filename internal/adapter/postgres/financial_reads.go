package postgres

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/junglegaming/backend-challenge-go/internal/domain/wager"
	"github.com/junglegaming/backend-challenge-go/internal/domain/wallet"
	"github.com/junglegaming/backend-challenge-go/internal/ports"
)

type financialReader struct{ pool *pgxpool.Pool }

func NewFinancialReader(pool *pgxpool.Pool) ports.FinancialReader {
	return &financialReader{pool: pool}
}

func (r *financialReader) ListLedger(ctx context.Context, id wallet.ID, after ports.LedgerPosition, limit int) ([]ports.LedgerEntry, error) {
	rows, err := r.pool.Query(ctx, `SELECT id,wallet_id,transaction_id,direction,amount_cents,balance_before_cents,balance_after_cents,wallet_version,created_at FROM wallet_ledger_entries WHERE wallet_id=$1 AND (wallet_version,id COLLATE "C") > ($2,$3 COLLATE "C") ORDER BY wallet_version,id COLLATE "C" LIMIT $4`, string(id), after.WalletVersion, after.EntryID, limit)
	if err != nil {
		return nil, mapError(err)
	}
	defer rows.Close()
	entries := make([]ports.LedgerEntry, 0)
	for rows.Next() {
		var e ports.LedgerEntry
		if err := rows.Scan(&e.ID, &e.WalletID, &e.TransactionID, &e.Direction, &e.AmountCents, &e.BalanceBeforeCents, &e.BalanceAfterCents, &e.WalletVersion, &e.CreatedAt); err != nil {
			return nil, mapError(err)
		}
		entries = append(entries, e)
	}
	if err := rows.Err(); err != nil {
		return nil, mapError(err)
	}
	return entries, nil
}

func (r *financialReader) GetTransactionSnapshot(ctx context.Context, id wager.TransactionID) (ports.TransactionSnapshot, error) {
	var w wagerRow
	var result []byte
	err := r.pool.QueryRow(ctx, `SELECT t.transaction_id,coalesce(t.provider_id,''),coalesce(t.external_transaction_id,''),t.player_id,t.wallet_id,t.currency,t.type,t.amount_cents,coalesce(t.round_id,''),coalesce(t.reference_external_transaction_id,''),t.state,coalesce(t.failure_code,''),t.created_at,t.updated_at,coalesce(t.game_id,''),i.result FROM wager_transactions t LEFT JOIN LATERAL (SELECT result FROM wager_idempotency_records WHERE transaction_id=t.transaction_id AND completed_at IS NOT NULL ORDER BY created_at DESC,idempotency_key LIMIT 1) i ON true WHERE t.transaction_id=$1`, string(id)).Scan(&w.id, &w.provider, &w.external, &w.player, &w.wallet, &w.currency, &w.kind, &w.amount, &w.round, &w.reference, &w.state, &w.failure, &w.created, &w.updated, &w.game, &result)
	if err != nil {
		return ports.TransactionSnapshot{}, mapError(err)
	}
	tx, err := w.domain()
	if err != nil {
		return ports.TransactionSnapshot{}, ports.ErrPersistence
	}
	return ports.TransactionSnapshot{Transaction: tx, SavedResult: result}, nil
}
