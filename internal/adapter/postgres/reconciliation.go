package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/junglegaming/backend-challenge-go/internal/domain/wallet"
	"github.com/junglegaming/backend-challenge-go/internal/ports"
)

type reconciliationReader struct{ db transactionBeginner }

func NewReconciliationReader(pool *pgxpool.Pool) ports.ReconciliationReader {
	return &reconciliationReader{db: pool}
}

func (r *reconciliationReader) ReadReconciliation(ctx context.Context, id wallet.ID) (result ports.ReconciliationSnapshot, err error) {
	tx, err := r.db.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return ports.ReconciliationSnapshot{}, mapError(err)
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if e := tx.Rollback(cleanup); e != nil && !errors.Is(e, pgx.ErrTxClosed) {
			result, err = ports.ReconciliationSnapshot{}, mapError(e)
		}
	}()
	result.Wallet, err = scanWallet(tx.QueryRow(ctx, `SELECT id,player_id,currency,balance_cents,version,created_at,updated_at FROM wallets WHERE id=$1`, string(id)))
	if err != nil {
		return ports.ReconciliationSnapshot{}, err
	}
	rows, err := tx.Query(ctx, `SELECT id,wallet_id,transaction_id,direction,amount_cents,balance_before_cents,balance_after_cents,wallet_version,created_at FROM wallet_ledger_entries WHERE wallet_id=$1 ORDER BY wallet_version,id COLLATE "C"`, string(id))
	if err != nil {
		return ports.ReconciliationSnapshot{}, mapError(err)
	}
	result.Ledger = make([]ports.LedgerEntry, 0)
	for rows.Next() {
		var e ports.LedgerEntry
		if err = rows.Scan(&e.ID, &e.WalletID, &e.TransactionID, &e.Direction, &e.AmountCents, &e.BalanceBeforeCents, &e.BalanceAfterCents, &e.WalletVersion, &e.CreatedAt); err != nil {
			break
		}
		result.Ledger = append(result.Ledger, e)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return ports.ReconciliationSnapshot{}, mapError(err)
	}
	// UNION keeps the selected set wallet-scoped and deduplicated. Reference
	// lookups use the existing (provider_id,external_transaction_id) index.
	rows, err = tx.Query(ctx, `WITH relevant AS (
	 SELECT transaction_id FROM wager_transactions WHERE wallet_id=$1
	 UNION SELECT transaction_id FROM wallet_ledger_entries WHERE wallet_id=$1
	 UNION SELECT ref.transaction_id FROM wager_transactions own
	 JOIN wager_transactions ref ON ref.provider_id=own.provider_id AND ref.external_transaction_id=own.reference_external_transaction_id
	 WHERE own.wallet_id=$1 AND own.type IN ('WIN','REFUND','ROLLBACK')
	) SELECT t.transaction_id,coalesce(t.provider_id,''),coalesce(t.external_transaction_id,''),t.player_id,t.wallet_id,t.currency,t.type,t.amount_cents,coalesce(t.round_id,''),coalesce(t.reference_external_transaction_id,''),t.state
	 FROM relevant JOIN wager_transactions t USING(transaction_id) ORDER BY t.transaction_id COLLATE "C"`, string(id))
	if err != nil {
		return ports.ReconciliationSnapshot{}, mapError(err)
	}
	result.Transactions = make([]ports.AuditTransaction, 0)
	for rows.Next() {
		var t ports.AuditTransaction
		if err = rows.Scan(&t.ID, &t.ProviderID, &t.ExternalID, &t.PlayerID, &t.WalletID, &t.Currency, &t.Type, &t.AmountCents, &t.RoundID, &t.ReferenceExternalID, &t.State); err != nil {
			break
		}
		result.Transactions = append(result.Transactions, t)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return ports.ReconciliationSnapshot{}, mapError(err)
	}
	if err = tx.Commit(ctx); err != nil {
		return ports.ReconciliationSnapshot{}, mapError(err)
	}
	return result, nil
}
