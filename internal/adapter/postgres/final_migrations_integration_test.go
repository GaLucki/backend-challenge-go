package postgres

import (
	"fmt"
	"testing"

	"github.com/junglegaming/backend-challenge-go/internal/application/financial"
)

func TestIntegrationFinalMigrationsEntireChainUpDownUp(t *testing.T) {
	pool, ctx := phase7Pool(t) // disposable schema: never roll back public
	up := []string{"000001_app_metadata", "000002_financial_persistence", "000003_wager_idempotency", "000004_reversals_pending_references", "000005_outbox_publication_leases", "000006_final_contract_metadata"}
	for i := len(up) - 1; i >= 0; i-- {
		applyMigration(t, ctx, pool, up[i]+".down.sql")
	}
	var remaining int
	must(t, pool.QueryRow(ctx, `SELECT count(*) FROM pg_class WHERE relnamespace=current_schema()::regnamespace`).Scan(&remaining))
	if remaining != 0 {
		t.Fatal("migration DOWN left relations behind", remaining)
	}
	must(t, pool.QueryRow(ctx, `SELECT count(*) FROM pg_proc WHERE pronamespace=current_schema()::regnamespace`).Scan(&remaining))
	if remaining != 0 {
		t.Fatal("migration DOWN left functions behind", remaining)
	}
	for _, name := range up {
		applyMigration(t, ctx, pool, name+".up.sql")
	}
	core := financial.NewService(NewUnitOfWork(pool))
	w := createFinancialWallet(t, ctx, core, "migration-player", 10000)
	for _, sql := range []string{
		fmt.Sprintf(`UPDATE wager_transactions SET amount_cents=1 WHERE transaction_id='%s'`, w.OpeningTransactionID),
		fmt.Sprintf(`UPDATE wager_transactions SET state='PENDING' WHERE transaction_id='%s'`, w.OpeningTransactionID),
		fmt.Sprintf(`DELETE FROM wager_transactions WHERE transaction_id='%s'`, w.OpeningTransactionID),
		`INSERT INTO wager_transactions (transaction_id,player_id,wallet_id,currency,type,amount_cents,state,created_at,updated_at) SELECT 'duplicate-opening',player_id,wallet_id,currency,'OPENING',10000,'PROCESSED',created_at,updated_at FROM wager_transactions WHERE type='OPENING'`,
	} {
		if _, err := pool.Exec(ctx, sql); err == nil {
			t.Fatalf("invalid financial mutation accepted: %s", sql)
		}
	}
	verifyWallet(t, ctx, pool, w.WalletID, 10000, 1)
	countRows(t, ctx, pool, "wallet_ledger_entries", 1)
	countRows(t, ctx, pool, "outbox_events", 2)
}
