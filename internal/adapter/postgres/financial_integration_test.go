package postgres

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/junglegaming/backend-challenge-go/internal/domain/money"
	"github.com/junglegaming/backend-challenge-go/internal/domain/wager"
	"github.com/junglegaming/backend-challenge-go/internal/domain/wallet"
	"github.com/junglegaming/backend-challenge-go/internal/ports"
)

// Opt-in against a real PostgreSQL instance. Each test owns a disposable schema.
func integrationPool(t *testing.T) (*pgxpool.Pool, context.Context) {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("set TEST_DATABASE_URL to run real PostgreSQL integration tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	admin, err := pgx.Connect(ctx, url)
	must(t, err)
	token := make([]byte, 8)
	_, err = rand.Read(token)
	must(t, err)
	schema := "phase3_" + hex.EncodeToString(token)
	quoted := pgx.Identifier{schema}.Sanitize()
	_, err = admin.Exec(ctx, "CREATE SCHEMA "+quoted)
	must(t, err)
	cfg, err := pgxpool.ParseConfig(url)
	must(t, err)
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	cfg.MaxConns = 8
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	must(t, err)
	t.Cleanup(func() {
		pool.Close()
		cleanup, c := context.WithTimeout(context.Background(), 5*time.Second)
		defer c()
		_, err := admin.Exec(cleanup, "DROP SCHEMA "+quoted+" CASCADE")
		if err != nil {
			t.Errorf("schema cleanup: %v", err)
		}
		_ = admin.Close(cleanup)
	})
	applyMigration(t, ctx, pool, "000001_app_metadata.up.sql")
	applyMigration(t, ctx, pool, "000002_financial_persistence.up.sql")
	return pool, ctx
}
func applyMigration(t *testing.T, ctx context.Context, pool *pgxpool.Pool, name string) {
	t.Helper()
	sql, err := os.ReadFile(filepath.Join("..", "..", "..", "migrations", name))
	must(t, err)
	_, err = pool.Exec(ctx, string(sql))
	must(t, err)
}
func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
func expectError(t *testing.T, err, want error) {
	t.Helper()
	if !errors.Is(err, want) {
		t.Fatalf("got %v, want %v", err, want)
	}
}
func testWallet(t *testing.T, id, player string) wallet.Wallet {
	t.Helper()
	m, err := money.New(1000, "BRL")
	must(t, err)
	w, err := wallet.New(wallet.ID(id), wallet.PlayerID(player), "BRL", m)
	must(t, err)
	return w
}
func testWager(t *testing.T, id, external string) wager.Transaction {
	t.Helper()
	m, err := money.New(100, "BRL")
	must(t, err)
	tx, err := wager.NewExternal(wager.ExternalParams{ID: wager.TransactionID(id), ProviderID: "provider", ExternalTransactionID: wager.ExternalTransactionID(external), PlayerID: "player", WalletID: "wallet", RoundID: "round", Type: wager.TypeBet, Amount: m, Now: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)})
	must(t, err)
	return tx
}
func testLedger(id, transaction string) ports.LedgerEntry {
	return ports.LedgerEntry{ID: id, WalletID: "wallet", TransactionID: wager.TransactionID(transaction), Direction: ports.Debit, AmountCents: 100, BalanceBeforeCents: 1000, BalanceAfterCents: 900, WalletVersion: 2, CreatedAt: time.Now().UTC()}
}
func testInbox() ports.InboxMessage {
	return ports.InboxMessage{ConsumerName: "consumer", MessageID: "message", PayloadHash: strings.Repeat("a", 64), ReceivedAt: time.Now().UTC()}
}
func testOutbox() ports.OutboxEvent {
	return ports.OutboxEvent{EventID: "event", AggregateID: "wallet", EventType: "wallet.changed", Payload: json.RawMessage(`{"amount_cents":100,"nested":{"ok":true}}`), OccurredAt: time.Now().UTC(), NextAttemptAt: time.Now().UTC()}
}

func TestIntegrationMigrationsUpDownUp(t *testing.T) {
	pool, ctx := integrationPool(t)
	applyMigration(t, ctx, pool, "000002_financial_persistence.down.sql")
	for _, name := range []string{"wallets", "wager_transactions", "wallet_ledger_entries", "inbox_messages", "outbox_events"} {
		var exists bool
		must(t, pool.QueryRow(ctx, `SELECT to_regclass($1) IS NOT NULL`, name).Scan(&exists))
		if exists {
			t.Fatal(name + " survived DOWN")
		}
	}
	var count int
	must(t, pool.QueryRow(ctx, `SELECT count(*) FROM pg_proc WHERE proname='reject_ledger_mutation' AND pronamespace=current_schema()::regnamespace`).Scan(&count))
	if count != 0 {
		t.Fatal("function survived DOWN")
	}
	applyMigration(t, ctx, pool, "000001_app_metadata.down.sql")
	applyMigration(t, ctx, pool, "000001_app_metadata.up.sql")
	applyMigration(t, ctx, pool, "000002_financial_persistence.up.sql")
	must(t, NewRepositories(pool).Wallets.Create(ctx, testWallet(t, "wallet", "player")))
}
func TestIntegrationRepositoriesAndConstraints(t *testing.T) {
	pool, ctx := integrationPool(t)
	rs := NewRepositories(pool)
	w := testWallet(t, "wallet", "player")
	must(t, rs.Wallets.Create(ctx, w))
	loaded, err := rs.Wallets.Get(ctx, w.ID())
	must(t, err)
	if loaded != w {
		t.Fatal("wallet round trip mismatch")
	}
	_, err = rs.Wallets.Get(ctx, "missing")
	expectError(t, err, ports.ErrNotFound)
	expectError(t, rs.Wallets.Create(ctx, testWallet(t, "duplicate", "player")), ports.ErrUniqueViolation)
	for _, sql := range []string{`UPDATE wallets SET balance_cents=-1 WHERE id='wallet'`, `UPDATE wallets SET version=0 WHERE id='wallet'`, `UPDATE wallets SET currency='brl' WHERE id='wallet'`} {
		_, err := pool.Exec(ctx, sql)
		expectError(t, mapError(err), ports.ErrConstraintViolation)
	}
	tx := testWager(t, "tx", "external")
	must(t, rs.Wagers.Create(ctx, tx))
	got, err := rs.Wagers.Get(ctx, "tx")
	must(t, err)
	if got != tx {
		t.Fatal("wager round trip mismatch")
	}
	got, err = rs.Wagers.GetByExternalID(ctx, "provider", "external")
	must(t, err)
	if got != tx {
		t.Fatal("external lookup mismatch")
	}
	expectError(t, rs.Wagers.Create(ctx, testWager(t, "duplicate-tx", "external")), ports.ErrUniqueViolation)
	m, _ := money.New(0, "BRL")
	for _, id := range []wager.TransactionID{"opening1", "opening2"} {
		opening, err := wager.NewOpening(wager.OpeningParams{ID: id, PlayerID: "player", WalletID: "wallet", Amount: m, Now: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)})
		must(t, err)
		must(t, rs.Wagers.Create(ctx, opening))
		restored, err := rs.Wagers.Get(ctx, id)
		must(t, err)
		if restored != opening {
			t.Fatal("opening mismatch")
		}
	}
	entry := testLedger("entry", "tx")
	must(t, rs.Ledger.Append(ctx, entry))
	gotEntry, err := rs.Ledger.Get(ctx, "entry")
	must(t, err)
	if gotEntry.AmountCents != entry.AmountCents || gotEntry.Direction != entry.Direction || !gotEntry.CreatedAt.Equal(entry.CreatedAt.Truncate(time.Microsecond)) {
		t.Fatal("ledger mismatch")
	}
	entry.ID = "duplicate-entry"
	expectError(t, rs.Ledger.Append(ctx, entry), ports.ErrUniqueViolation)
	for _, sql := range []string{
		`UPDATE wager_transactions SET amount_cents=-1 WHERE transaction_id='tx'`,
		`UPDATE wager_transactions SET type='UNKNOWN' WHERE transaction_id='tx'`,
		`UPDATE wager_transactions SET state='UNKNOWN' WHERE transaction_id='tx'`,
		`UPDATE wager_transactions SET wallet_id='missing' WHERE transaction_id='tx'`,
		`UPDATE wager_transactions SET player_id='wrong-player' WHERE transaction_id='tx'`,
		`UPDATE wager_transactions SET currency='USD' WHERE transaction_id='tx'`,
	} {
		_, err := pool.Exec(ctx, sql)
		expectError(t, mapError(err), ports.ErrConstraintViolation)
	}
	for _, invalid := range []ports.LedgerEntry{
		{ID: "invalid-direction", WalletID: "wallet", TransactionID: "tx", Direction: "OTHER", AmountCents: 100, BalanceBeforeCents: 1000, BalanceAfterCents: 900, WalletVersion: 2},
		{ID: "invalid-amount", WalletID: "wallet", TransactionID: "tx", Direction: ports.Debit, AmountCents: 0, BalanceBeforeCents: 1000, BalanceAfterCents: 1000, WalletVersion: 2},
		{ID: "invalid-balance", WalletID: "wallet", TransactionID: "tx", Direction: ports.Debit, AmountCents: 100, BalanceBeforeCents: 0, BalanceAfterCents: -100, WalletVersion: 2},
		{ID: "invalid-version", WalletID: "wallet", TransactionID: "tx", Direction: ports.Debit, AmountCents: 100, BalanceBeforeCents: 1000, BalanceAfterCents: 900, WalletVersion: 0},
		{ID: "invalid-fk", WalletID: "wallet", TransactionID: "missing", Direction: ports.Debit, AmountCents: 100, BalanceBeforeCents: 1000, BalanceAfterCents: 900, WalletVersion: 2},
	} {
		invalid.CreatedAt = time.Now().UTC()
		expectError(t, rs.Ledger.Append(ctx, invalid), ports.ErrConstraintViolation)
	}
	for _, sql := range []string{`UPDATE wallet_ledger_entries SET amount_cents=1`, `DELETE FROM wallet_ledger_entries`, `TRUNCATE wallet_ledger_entries`} {
		_, err := pool.Exec(ctx, sql)
		expectError(t, mapError(err), ports.ErrConstraintViolation)
	}
	inbox := testInbox()
	must(t, rs.Inbox.Create(ctx, inbox))
	expectError(t, rs.Inbox.Create(ctx, inbox), ports.ErrUniqueViolation)
	gotInbox, err := rs.Inbox.Get(ctx, inbox.ConsumerName, inbox.MessageID)
	must(t, err)
	if gotInbox.PayloadHash != inbox.PayloadHash || gotInbox.CompletedAt != nil {
		t.Fatal("inbox mismatch")
	}
	at := time.Now().UTC().Truncate(time.Microsecond)
	must(t, rs.Inbox.Complete(ctx, inbox.ConsumerName, inbox.MessageID, at))
	gotInbox, err = rs.Inbox.Get(ctx, inbox.ConsumerName, inbox.MessageID)
	must(t, err)
	if gotInbox.CompletedAt == nil || !gotInbox.CompletedAt.Equal(at) {
		t.Fatal("completion mismatch")
	}
	event := testOutbox()
	must(t, rs.Outbox.Create(ctx, event))
	out, err := rs.Outbox.Get(ctx, event.EventID)
	must(t, err)
	var original, stored any
	must(t, json.Unmarshal(event.Payload, &original))
	must(t, json.Unmarshal(out.Payload, &stored))
	if stringMustJSON(t, original) != stringMustJSON(t, stored) {
		t.Fatal("JSONB mismatch")
	}
	expectError(t, rs.Outbox.Create(ctx, event), ports.ErrUniqueViolation)
	event.EventID = "invalid-retry"
	event.RetryCount = -1
	expectError(t, rs.Outbox.Create(ctx, event), ports.ErrConstraintViolation)
	must(t, NewUnitOfWork(pool).WithinTransaction(ctx, func(r ports.Repositories) error {
		locked, err := r.Wallets.GetForUpdate(ctx, "wallet")
		if err != nil {
			return err
		}
		version := locked.Version()
		if err = locked.Debit(tx.Amount()); err != nil {
			return err
		}
		if err = r.Wallets.Update(ctx, locked, version); err != nil {
			return err
		}
		if err = tx.MarkProcessed(at); err != nil {
			return err
		}
		return r.Wagers.Update(ctx, tx, wager.StatePending)
	}))
	updated, err := rs.Wallets.Get(ctx, "wallet")
	must(t, err)
	if updated.Balance().Cents() != 900 || updated.Version() != 2 {
		t.Fatal("wallet update mismatch")
	}
	got, err = rs.Wagers.Get(ctx, "tx")
	must(t, err)
	if got.State() != wager.StateProcessed {
		t.Fatal("wager update mismatch")
	}
	expectError(t, NewUnitOfWork(pool).WithinTransaction(ctx, func(r ports.Repositories) error { return r.Wallets.Update(ctx, updated, 1) }), ports.ErrConflict)
	expectError(t, NewUnitOfWork(pool).WithinTransaction(ctx, func(r ports.Repositories) error { return r.Wagers.Update(ctx, got, wager.StatePending) }), ports.ErrConflict)
}
func stringMustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	must(t, err)
	return string(b)
}

func TestIntegrationAtomicRollback(t *testing.T) {
	pool, ctx := integrationPool(t)
	rs := NewRepositories(pool)
	must(t, rs.Wallets.Create(ctx, testWallet(t, "wallet", "player")))
	existing := testWager(t, "existing", "existing-ext")
	must(t, rs.Wagers.Create(ctx, existing))
	sentinel := errors.New("abort financial transaction")
	err := NewUnitOfWork(pool).WithinTransaction(ctx, func(r ports.Repositories) error {
		w, err := r.Wallets.GetForUpdate(ctx, "wallet")
		if err != nil {
			return err
		}
		version := w.Version()
		amount, _ := money.New(100, "BRL")
		if err = w.Debit(amount); err != nil {
			return err
		}
		if err = r.Wallets.Update(ctx, w, version); err != nil {
			return err
		}
		if err = r.Wallets.Create(ctx, testWallet(t, "new-wallet", "new-player")); err != nil {
			return err
		}
		if err = existing.MarkProcessed(time.Now()); err != nil {
			return err
		}
		if err = r.Wagers.Update(ctx, existing, wager.StatePending); err != nil {
			return err
		}
		if err = r.Wagers.Create(ctx, testWager(t, "tx", "external")); err != nil {
			return err
		}
		if err = r.Ledger.Append(ctx, testLedger("entry", "tx")); err != nil {
			return err
		}
		if err = r.Inbox.Create(ctx, testInbox()); err != nil {
			return err
		}
		if err = r.Outbox.Create(ctx, testOutbox()); err != nil {
			return err
		}
		return sentinel
	})
	expectError(t, err, sentinel)
	w, err := rs.Wallets.Get(ctx, "wallet")
	must(t, err)
	if w.Balance().Cents() != 1000 || w.Version() != 1 {
		t.Fatal("wallet change survived rollback")
	}
	tx, err := rs.Wagers.Get(ctx, "existing")
	must(t, err)
	if tx.State() != wager.StatePending {
		t.Fatal("state change survived rollback")
	}
	for _, table := range []string{"wallets", "wager_transactions", "wallet_ledger_entries", "inbox_messages", "outbox_events"} {
		var count int
		must(t, pool.QueryRow(ctx, "SELECT count(*) FROM "+table).Scan(&count))
		want := 0
		if table == "wallets" || table == "wager_transactions" {
			want = 1
		}
		if count != want {
			t.Fatalf("%s count=%d want=%d", table, count, want)
		}
	}
}

func TestIntegrationWalletRowLocks(t *testing.T) {
	pool, ctx := integrationPool(t)
	rs := NewRepositories(pool)
	must(t, rs.Wallets.Create(ctx, testWallet(t, "wallet", "player")))
	must(t, rs.Wallets.Create(ctx, testWallet(t, "other", "other-player")))
	holder, err := pool.Begin(ctx)
	must(t, err)
	defer holder.Rollback(context.Background())
	_, err = repositories(holder, true).Wallets.GetForUpdate(ctx, "wallet")
	must(t, err)
	contender, err := pool.Begin(ctx)
	must(t, err)
	defer contender.Rollback(context.Background())
	var pid int32
	must(t, contender.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&pid))
	done := make(chan error, 1)
	go func() { _, err := repositories(contender, true).Wallets.GetForUpdate(ctx, "wallet"); done <- err }()
	// Observe PostgreSQL's actual lock wait, avoiding timing-based assertions.
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		var waiting bool
		must(t, pool.QueryRow(ctx, `SELECT coalesce(wait_event_type='Lock',false) FROM pg_stat_activity WHERE pid=$1`, pid).Scan(&waiting))
		if waiting {
			break
		}
		select {
		case err := <-done:
			t.Fatalf("same wallet did not block: %v", err)
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-ticker.C:
		}
	}
	otherCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	must(t, NewUnitOfWork(pool).WithinTransaction(otherCtx, func(r ports.Repositories) error {
		w, err := r.Wallets.GetForUpdate(otherCtx, "other")
		if err != nil {
			return err
		}
		version := w.Version()
		m, _ := money.New(1, "BRL")
		if err = w.Credit(m); err != nil {
			return err
		}
		return r.Wallets.Update(otherCtx, w, version)
	}))
	select {
	case err := <-done:
		t.Fatalf("lock released too soon: %v", err)
	default:
	}
	must(t, holder.Commit(ctx))
	select {
	case err := <-done:
		must(t, err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	must(t, contender.Commit(ctx))
}
