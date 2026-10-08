package postgres

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/junglegaming/backend-challenge-go/internal/application/financial"
	"github.com/junglegaming/backend-challenge-go/internal/domain/wager"
	"github.com/junglegaming/backend-challenge-go/internal/domain/wallet"
	"github.com/junglegaming/backend-challenge-go/internal/identity"
	"github.com/junglegaming/backend-challenge-go/internal/ports"
)

func auditInternal() identity.Principal {
	return identity.Principal{Subject: "internal", ClientID: "internal", Internal: true, Roles: []string{identity.RoleInternal}}
}
func reconcileWallet(t *testing.T, ctx context.Context, pool *pgxpool.Pool, id wallet.ID) financial.ReconciliationResult {
	t.Helper()
	r, err := financial.NewReconciliationService(NewReconciliationReader(pool), identity.NewAuthorizer()).Reconcile(ctx, auditInternal(), id)
	must(t, err)
	return r
}
func consistentAudit(t *testing.T, r financial.ReconciliationResult, balance string, version int64, entries int) {
	t.Helper()
	if r.Status != "CONSISTENT" || len(r.Divergences) != 0 || r.WalletBalance.Amount != balance || r.LedgerBalance == nil || r.LedgerBalance.Amount != balance || r.WalletVersion != version || r.ExpectedWalletVersion == nil || *r.ExpectedWalletVersion != version || r.LedgerEntriesChecked != entries || r.CheckedAt.IsZero() {
		t.Fatalf("bad audit: %+v", r)
	}
}

// Exact JSON of every row, not only counts, proves an audit changes neither
// financial state nor messaging/idempotency/recovery evidence.
func financialDatabaseEvidence(t *testing.T, ctx context.Context, pool *pgxpool.Pool) string {
	t.Helper()
	var evidence strings.Builder
	for _, table := range []string{"wallets", "wager_transactions", "wallet_ledger_entries", "outbox_events", "inbox_messages", "wager_idempotency_records", "wager_reversals", "pending_wager_references"} {
		var raw string
		must(t, pool.QueryRow(ctx, `SELECT coalesce(jsonb_agg(row ORDER BY row::text),'[]'::jsonb)::text FROM (SELECT to_jsonb(t) AS row FROM `+table+` t) s`).Scan(&raw))
		evidence.WriteString(table + raw)
	}
	return evidence.String()
}

func TestIntegrationReconciliationAllOperationsReadOnlyAndLargeHistory(t *testing.T) {
	pool, ctx := phase5Pool(t)
	s := financial.NewService(NewUnitOfWork(pool))
	zero := createFinancialWallet(t, ctx, s, "zero-audit", 0)
	consistentAudit(t, reconcileWallet(t, ctx, pool, zero.WalletID), "0.00", 1, 0)
	w := createFinancialWallet(t, ctx, s, "audit-player", 10000)
	consistentAudit(t, reconcileWallet(t, ctx, pool, w.WalletID), "100.00", 1, 1)
	for _, step := range []struct {
		external     string
		kind         wager.Type
		amount       int64
		ref, balance string
		version      int64
		entries      int
	}{
		{"bet", wager.TypeBet, 2000, "", "80.00", 2, 2},
		{"win", wager.TypeWin, 5000, "", "130.00", 3, 3},
		{"loss", wager.TypeLoss, 0, "", "130.00", 3, 3},
		{"refund", wager.TypeRefund, 2000, "bet", "150.00", 4, 4},
		{"rollback-refund", wager.TypeRollback, 2000, "refund", "130.00", 5, 5},
		{"rollback-win", wager.TypeRollback, 5000, "win", "80.00", 6, 6},
		{"bet-2", wager.TypeBet, 1000, "", "70.00", 7, 7},
		{"rollback-bet", wager.TypeRollback, 1000, "bet-2", "80.00", 8, 8},
	} {
		runFinancial(t, ctx, s, referenceInput(t, w.WalletID, "audit-player", step.external, step.external, step.kind, step.amount, step.ref))
		consistentAudit(t, reconcileWallet(t, ctx, pool, w.WalletID), step.balance, step.version, step.entries)
	}
	rejected := runFinancial(t, ctx, s, financialInput(t, w.WalletID, "audit-player", "rejected", "rejected", wager.TypeBet, 100000))
	if rejected.State != wager.StateRejected {
		t.Fatal("BET not rejected")
	}
	consistentAudit(t, reconcileWallet(t, ctx, pool, w.WalletID), "80.00", 8, 8)
	pending := runFinancial(t, ctx, s, referenceInput(t, w.WalletID, "audit-player", "pending", "pending", wager.TypeRefund, 500, "future"))
	if pending.State != wager.StatePendingReference {
		t.Fatal("not pending")
	}
	consistentAudit(t, reconcileWallet(t, ctx, pool, w.WalletID), "80.00", 8, 8)
	runFinancial(t, ctx, s, financialInput(t, w.WalletID, "audit-player", "future", "future", wager.TypeBet, 500))
	handled, err := s.ResolvePendingOnce(ctx, time.Now().UTC().Add(2*time.Second))
	must(t, err)
	if !handled {
		t.Fatal("pending not resolved")
	}
	consistentAudit(t, reconcileWallet(t, ctx, pool, w.WalletID), "80.00", 10, 10)
	// The first financial movement of a zero-created wallet is version 2.
	runFinancial(t, ctx, s, financialInput(t, zero.WalletID, "zero-audit", "first-win", "first-win", wager.TypeWin, 100))
	consistentAudit(t, reconcileWallet(t, ctx, pool, zero.WalletID), "1.00", 2, 1)
	for i := 0; i < 120; i++ {
		external := fmt.Sprintf("large-%03d", i)
		runFinancial(t, ctx, s, financialInput(t, w.WalletID, "audit-player", external, external, wager.TypeWin, 1))
	}
	before := financialDatabaseEvidence(t, ctx, pool)
	r := reconcileWallet(t, ctx, pool, w.WalletID)
	consistentAudit(t, r, "81.20", 130, 130)
	if r.TransactionsChecked != 132 {
		t.Fatal("incomplete transaction history", r.TransactionsChecked)
	}
	again := reconcileWallet(t, ctx, pool, w.WalletID)
	r.CheckedAt = again.CheckedAt
	if !reflect.DeepEqual(r, again) {
		t.Fatal("unchanged snapshot conclusion differs")
	}
	if financialDatabaseEvidence(t, ctx, pool) != before {
		t.Fatal("audit mutated persisted evidence")
	}
	_, err = NewReconciliationReader(pool).ReadReconciliation(ctx, "missing")
	if !errors.Is(err, ports.ErrNotFound) {
		t.Fatal("missing wallet", err)
	}
}

// Every corruption subtest owns a separate disposable schema. Trigger bypasses
// occur in one test transaction and are re-enabled before commit. Any dropped
// constraint belongs only to that schema, which cleanup destroys entirely.
func corruptAuditEvidence(t *testing.T, ctx context.Context, pool *pgxpool.Pool, sql string, args ...any) {
	t.Helper()
	var schema string
	must(t, pool.QueryRow(ctx, `SELECT current_schema()`).Scan(&schema))
	if !strings.HasPrefix(schema, "phase3_") {
		t.Fatal("corruption requires a disposable test schema")
	}
	tx, err := pool.Begin(ctx)
	must(t, err)
	defer tx.Rollback(context.Background())
	_, err = tx.Exec(ctx, `ALTER TABLE wallet_ledger_entries DISABLE TRIGGER wallet_ledger_append_only`)
	must(t, err)
	_, err = tx.Exec(ctx, `ALTER TABLE wager_transactions DISABLE TRIGGER wager_terminal_immutable`)
	must(t, err)
	// Simple protocol permits controlled DDL and a parameterized mutation in
	// the same fixture. It is never used by production queries.
	if !strings.Contains(sql, "$2") {
		args = args[:1]
	}
	if !strings.Contains(sql, "$1") {
		sql = strings.ReplaceAll(sql, "$2", "$1")
		args = args[1:]
	}
	_, err = tx.Exec(ctx, sql, append([]any{pgx.QueryExecModeSimpleProtocol}, args...)...)
	must(t, err)
	_, err = tx.Exec(ctx, `ALTER TABLE wallet_ledger_entries ENABLE TRIGGER wallet_ledger_append_only`)
	must(t, err)
	_, err = tx.Exec(ctx, `ALTER TABLE wager_transactions ENABLE TRIGGER wager_terminal_immutable`)
	must(t, err)
	must(t, tx.Commit(ctx))
}

func TestIntegrationReconciliationControlledCorruption(t *testing.T) {
	for _, tc := range []struct {
		name, sql string
		code      financial.DivergenceCode
	}{
		{"balance", `UPDATE wallets SET balance_cents=7900 WHERE id=$1`, financial.BalanceMismatch},
		{"chain", `UPDATE wallet_ledger_entries SET balance_before_cents=9900,balance_after_cents=7900 WHERE transaction_id=$2`, financial.LedgerChainBroken},
		{"amount", `UPDATE wallet_ledger_entries SET amount_cents=1900,balance_after_cents=8100 WHERE transaction_id=$2`, financial.LedgerAmountMismatch},
		{"direction", `UPDATE wallet_ledger_entries SET direction='CREDIT',balance_before_cents=0,balance_after_cents=2000 WHERE transaction_id=$2`, financial.LedgerDirectionMismatch},
		{"missing ledger", `DELETE FROM wallet_ledger_entries WHERE transaction_id=$2`, financial.TransactionLedgerMissing},
		{"unexpected loss", `UPDATE wager_transactions SET type='LOSS',amount_cents=0 WHERE transaction_id=$2`, financial.UnexpectedLedgerEntry},
		{"unexpected rejected", `UPDATE wager_transactions SET state='REJECTED',failure_code='TEST' WHERE transaction_id=$2`, financial.UnexpectedLedgerEntry},
		{"unexpected pending", `UPDATE wager_transactions SET state='PENDING_REFERENCE' WHERE transaction_id=$2`, financial.UnexpectedLedgerEntry},
		{"unexpected failed", `UPDATE wager_transactions SET state='FAILED',failure_code='TEST' WHERE transaction_id=$2`, financial.UnexpectedLedgerEntry},
		{"version", `UPDATE wallets SET version=9 WHERE id=$1`, financial.WalletVersionMismatch},
		{"ledger version", `UPDATE wallet_ledger_entries SET wallet_version=9 WHERE transaction_id=$2`, financial.WalletVersionMismatch},
		{"currency", `ALTER TABLE wager_transactions DROP CONSTRAINT wager_transactions_wallet_id_player_id_currency_fkey; UPDATE wager_transactions SET currency='USD' WHERE transaction_id=$2`, financial.LedgerCurrencyMismatch},
		{"missing transaction", `ALTER TABLE wallet_ledger_entries DROP CONSTRAINT wallet_ledger_entries_transaction_id_fkey; ALTER TABLE wallet_ledger_entries DROP CONSTRAINT wallet_ledger_entries_wallet_id_transaction_id_fkey; UPDATE wallet_ledger_entries SET transaction_id='missing' WHERE transaction_id=$2`, financial.LedgerTransactionNotFound},
		{"duplicate", `ALTER TABLE wallet_ledger_entries DROP CONSTRAINT wallet_ledger_entries_wallet_id_transaction_id_key; INSERT INTO wallet_ledger_entries SELECT id||':duplicate',wallet_id,transaction_id,direction,amount_cents,balance_before_cents,balance_after_cents,wallet_version,created_at FROM wallet_ledger_entries WHERE transaction_id=$2`, financial.DuplicateLedgerEntry},
		{"foreign transaction", `ALTER TABLE wallet_ledger_entries DROP CONSTRAINT wallet_ledger_entries_wallet_id_transaction_id_fkey; UPDATE wallet_ledger_entries SET transaction_id=(SELECT transaction_id FROM wager_transactions WHERE wallet_id<>$1 AND type='OPENING') WHERE transaction_id=$2`, financial.TransactionWalletMismatch},
		{"negative reconstructed balance", `ALTER TABLE wallet_ledger_entries DROP CONSTRAINT wallet_ledger_entries_balance_after_cents_check; UPDATE wallet_ledger_entries SET amount_cents=11000,balance_after_cents=-1000 WHERE transaction_id=$2`, financial.NegativeReconstructedBalance},
		{"arithmetic overflow", `ALTER TABLE wallet_ledger_entries DROP CONSTRAINT wallet_ledger_entries_check; UPDATE wallet_ledger_entries SET direction='CREDIT',amount_cents=9223372036854775807,balance_after_cents=8000 WHERE transaction_id=$2`, financial.LedgerArithmeticOverflow},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pool, ctx := phase5Pool(t)
			s := financial.NewService(NewUnitOfWork(pool))
			w := createFinancialWallet(t, ctx, s, "corrupt-player", 10000)
			createFinancialWallet(t, ctx, s, "foreign-player", 5000)
			bet := runFinancial(t, ctx, s, financialInput(t, w.WalletID, "corrupt-player", "bet", "bet", wager.TypeBet, 2000))
			corruptAuditEvidence(t, ctx, pool, tc.sql, string(w.WalletID), string(bet.TransactionID))
			before := financialDatabaseEvidence(t, ctx, pool)
			r := reconcileWallet(t, ctx, pool, w.WalletID)
			found := false
			for _, d := range r.Divergences {
				if d.Code == tc.code {
					found = true
				}
			}
			if r.Status != "DIVERGENT" || !found {
				t.Fatalf("missing %s: %+v", tc.code, r)
			}
			if financialDatabaseEvidence(t, ctx, pool) != before {
				t.Fatal("divergence audit repaired or changed evidence")
			}
			// Append-only protection was restored, even for corrupt history.
			_, err := pool.Exec(ctx, `UPDATE wallet_ledger_entries SET id=id WHERE false`)
			if err == nil {
				t.Fatal("test bypass left ledger mutable")
			}
		})
	}
}

// Wrapping pgx only in this test pauses immediately after the wallet SELECT
// establishes the real MVCC snapshot. Production has no hooks or sleeps.
type gatedAuditBeginner struct {
	pool              *pgxpool.Pool
	snapshot, release chan struct{}
}

func (b *gatedAuditBeginner) BeginTx(ctx context.Context, opts pgx.TxOptions) (pgx.Tx, error) {
	if opts.IsoLevel != pgx.RepeatableRead || opts.AccessMode != pgx.ReadOnly {
		return nil, fmt.Errorf("audit is not repeatable-read/read-only")
	}
	tx, err := b.pool.BeginTx(ctx, opts)
	if err != nil {
		return nil, err
	}
	return &gatedAuditTx{Tx: tx, snapshot: b.snapshot, release: b.release}, nil
}

type gatedAuditTx struct {
	pgx.Tx
	snapshot, release chan struct{}
}

func (tx *gatedAuditTx) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	return gatedAuditRow{row: tx.Tx.QueryRow(ctx, sql, args...), ctx: ctx, snapshot: tx.snapshot, release: tx.release}
}

type gatedAuditRow struct {
	row               pgx.Row
	ctx               context.Context
	snapshot, release chan struct{}
}

func (r gatedAuditRow) Scan(dest ...any) error {
	if err := r.row.Scan(dest...); err != nil {
		return err
	}
	close(r.snapshot)
	select {
	case <-r.release:
		return nil
	case <-r.ctx.Done():
		return r.ctx.Err()
	}
}

func TestIntegrationReconciliationConcurrentSnapshots(t *testing.T) {
	for _, same := range []bool{true, false} {
		t.Run(fmt.Sprintf("same_wallet_%t", same), func(t *testing.T) {
			pool, ctx := phase5Pool(t)
			s := financial.NewService(NewUnitOfWork(pool))
			wa := createFinancialWallet(t, ctx, s, "wallet-a", 10000)
			wb := createFinancialWallet(t, ctx, s, "wallet-b", 10000)
			gate := &gatedAuditBeginner{pool: pool, snapshot: make(chan struct{}), release: make(chan struct{})}
			defer close(gate.release)
			reader := &reconciliationReader{db: gate}
			type outcome struct {
				snapshot ports.ReconciliationSnapshot
				err      error
			}
			done := make(chan outcome, 1)
			go func() { snap, err := reader.ReadReconciliation(ctx, wa.WalletID); done <- outcome{snap, err} }()
			select {
			case <-gate.snapshot:
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			id, player := wb.WalletID, "wallet-b"
			if same {
				id, player = wa.WalletID, "wallet-a"
			}
			// Must commit while audit is deliberately paused; a row/global lock
			// would make this bounded write fail before releasing the audit.
			writeCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
			result, err := s.ProcessHTTPWager(writeCtx, financialInput(t, id, player, "concurrent", "concurrent", wager.TypeBet, 2000))
			cancel()
			must(t, err)
			if result.ObservedBalance.Amount != "80.00" {
				t.Fatal("concurrent write did not progress")
			}
			gate.release <- struct{}{}
			var read outcome
			select {
			case read = <-done:
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			must(t, read.err)
			service := financial.NewReconciliationService(snapshotAuditReader{snapshot: read.snapshot}, identity.NewAuthorizer())
			r, err := service.Reconcile(ctx, auditInternal(), wa.WalletID)
			must(t, err)
			consistentAudit(t, r, "100.00", 1, 1)
			if r.TransactionsChecked != 1 {
				t.Fatal("snapshot mixed transaction commits")
			}
			balance, version, entries := "100.00", int64(1), 1
			if same {
				balance, version, entries = "80.00", 2, 2
			}
			consistentAudit(t, reconcileWallet(t, ctx, pool, wa.WalletID), balance, version, entries)
		})
	}
}

type snapshotAuditReader struct{ snapshot ports.ReconciliationSnapshot }

func (r snapshotAuditReader) ReadReconciliation(context.Context, wallet.ID) (ports.ReconciliationSnapshot, error) {
	return r.snapshot, nil
}

func TestIntegrationReconciliationCanceledReadHasNoPartialResult(t *testing.T) {
	pool, ctx := phase5Pool(t)
	s := financial.NewService(NewUnitOfWork(pool))
	w := createFinancialWallet(t, ctx, s, "cancel-audit", 10000)
	gate := &gatedAuditBeginner{pool: pool, snapshot: make(chan struct{}), release: make(chan struct{})}
	canceled, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() {
		r, err := (&reconciliationReader{db: gate}).ReadReconciliation(canceled, w.WalletID)
		if r.Wallet.ID() != "" {
			err = fmt.Errorf("partial result returned: %v", err)
		}
		done <- err
	}()
	select {
	case <-gate.snapshot:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	// Canceled transactions return their pool connection after cleanup.
	must(t, pool.Ping(ctx))
}
