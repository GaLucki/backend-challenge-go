package postgres

import (
	"testing"

	"github.com/junglegaming/backend-challenge-go/internal/application/financial"
	"github.com/junglegaming/backend-challenge-go/internal/domain/wager"
)

func TestIntegrationPendingResultUpdateAtomicFailure(t *testing.T) {
	pool, ctx := phase5Pool(t)
	s := financial.NewService(NewUnitOfWork(pool))
	w := createFinancialWallet(t, ctx, s, "player", 10000)
	in := referenceInput(t, w.WalletID, "player", "refund", "refund-key", wager.TypeRefund, 2000, "bet")
	pending := runFinancial(t, ctx, s, in)
	at := readPending(t, ctx, pool, pending.TransactionID).NextAttemptAt
	runFinancial(t, ctx, s, financialInput(t, w.WalletID, "player", "bet", "bet-key", wager.TypeBet, 2000))
	_, err := pool.Exec(ctx, `CREATE FUNCTION test_fail_pending_result() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected result update failure' USING ERRCODE='23514'; END $$; CREATE TRIGGER test_pending_result_failure BEFORE UPDATE ON wager_idempotency_records FOR EACH ROW EXECUTE FUNCTION test_fail_pending_result();`)
	must(t, err)
	_, err = s.ResolvePendingOnce(ctx, at)
	expectError(t, err, financial.ErrPersistence)
	verifyWallet(t, ctx, pool, w.WalletID, 8000, 2)
	countRows(t, ctx, pool, "wager_reversals", 0)
	countRows(t, ctx, pool, "wallet_ledger_entries", 2)
	countRows(t, ctx, pool, "outbox_events", 5)
	result := runFinancial(t, ctx, s, in)
	if result.State != wager.StatePendingReference {
		t.Fatal("result partially changed")
	}
	p := readPending(t, ctx, pool, pending.TransactionID)
	if p.CompletedAt != nil || p.AttemptCount != 0 {
		t.Fatal("pending partially changed")
	}
	_, err = pool.Exec(ctx, `DROP TRIGGER test_pending_result_failure ON wager_idempotency_records`)
	must(t, err)
	handled, err := s.ResolvePendingOnce(ctx, at)
	must(t, err)
	if !handled {
		t.Fatal("pending not recovered")
	}
	verifyWallet(t, ctx, pool, w.WalletID, 10000, 3)
	result = runFinancial(t, ctx, s, in)
	if result.State != wager.StateProcessed || !result.IdempotentReplay {
		t.Fatal(result)
	}
}
func TestIntegrationOriginalConcurrentWithReversal(t *testing.T) {
	pool, ctx := phase5Pool(t)
	s := financial.NewService(NewUnitOfWork(pool))
	w := createFinancialWallet(t, ctx, s, "player", 10000)
	inputs := []financial.HTTPWagerInput{
		financialInput(t, w.WalletID, "player", "bet", "bet-key", wager.TypeBet, 2000),
		referenceInput(t, w.WalletID, "player", "refund", "refund-key", wager.TypeRefund, 2000, "bet"),
	}
	for _, out := range concurrentOperations(ctx, []*financial.Service{s, secondService(t, ctx, pool)}, inputs) {
		must(t, out.err)
		if out.result.Type == wager.TypeRefund && out.result.State == wager.StatePendingReference {
			at := readPending(t, ctx, pool, out.result.TransactionID).NextAttemptAt
			handled, err := s.ResolvePendingOnce(ctx, at)
			must(t, err)
			if !handled {
				t.Fatal("out-of-order race not resumed")
			}
		}
	}
	result := runFinancial(t, ctx, s, inputs[1])
	if result.State != wager.StateProcessed {
		t.Fatal(result)
	}
	verifyWallet(t, ctx, pool, w.WalletID, 10000, 3)
	countRows(t, ctx, pool, "wallet_ledger_entries", 3)
	countRows(t, ctx, pool, "wager_reversals", 1)
}
func TestIntegrationRollbackLOSSRejected(t *testing.T) {
	pool, ctx := phase5Pool(t)
	s := financial.NewService(NewUnitOfWork(pool))
	w := createFinancialWallet(t, ctx, s, "player", 10000)
	runFinancial(t, ctx, s, financialInput(t, w.WalletID, "player", "loss", "loss-key", wager.TypeLoss, 0))
	result := runFinancial(t, ctx, s, referenceInput(t, w.WalletID, "player", "rollback", "rollback-key", wager.TypeRollback, 0, "loss"))
	if result.State != wager.StateRejected || result.FailureCode != financial.FailureReferenceTypeInvalid {
		t.Fatal(result)
	}
	verifyWallet(t, ctx, pool, w.WalletID, 10000, 1)
	countRows(t, ctx, pool, "wallet_ledger_entries", 1)
	countRows(t, ctx, pool, "wager_reversals", 0)
}
