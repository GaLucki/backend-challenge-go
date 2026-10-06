package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/junglegaming/backend-challenge-go/internal/application/financial"
	"github.com/junglegaming/backend-challenge-go/internal/domain/money"
	"github.com/junglegaming/backend-challenge-go/internal/domain/wager"
	"github.com/junglegaming/backend-challenge-go/internal/domain/wallet"
	"github.com/junglegaming/backend-challenge-go/internal/ports"
)

func phase4Pool(t *testing.T) (*pgxpool.Pool, context.Context) {
	t.Helper()
	pool, ctx := integrationPool(t)
	applyMigration(t, ctx, pool, "000003_wager_idempotency.up.sql")
	return pool, ctx
}
func financialMoney(t *testing.T, n int64) money.Money {
	t.Helper()
	m, err := money.New(n, "BRL")
	must(t, err)
	return m
}
func createFinancialWallet(t *testing.T, ctx context.Context, s *financial.Service, player string, n int64) financial.CreateWalletResult {
	t.Helper()
	result, err := s.CreateWallet(ctx, financial.CreateWalletInput{PlayerID: wallet.PlayerID(player), Currency: "BRL", InitialBalance: financialMoney(t, n), CorrelationID: "correlation"})
	must(t, err)
	return result
}
func financialInput(t *testing.T, id wallet.ID, player, external, key string, kind wager.Type, n int64) financial.HTTPWagerInput {
	t.Helper()
	return financial.HTTPWagerInput{WagerInput: financial.WagerInput{ProviderID: "provider", ExternalTransactionID: wager.ExternalTransactionID(external), PlayerID: wager.PlayerID(player), WalletID: id, Type: kind, Amount: financialMoney(t, n), RoundID: "round", CorrelationID: "correlation"}, IdempotencyKey: key}
}
func countRows(t *testing.T, ctx context.Context, pool *pgxpool.Pool, table string, want int) {
	t.Helper()
	var count int
	must(t, pool.QueryRow(ctx, "SELECT count(*) FROM "+table).Scan(&count))
	if count != want {
		t.Fatalf("%s count=%d want=%d", table, count, want)
	}
}
func verifyWallet(t *testing.T, ctx context.Context, pool *pgxpool.Pool, id wallet.ID, balance, version int64) {
	t.Helper()
	w, err := NewRepositories(pool).Wallets.Get(ctx, id)
	must(t, err)
	if w.Balance().Cents() != balance || w.Version() != version {
		t.Fatalf("wallet balance=%d version=%d want=%d/%d", w.Balance().Cents(), w.Version(), balance, version)
	}
}
func verifyFinancialEvents(t *testing.T, ctx context.Context, pool *pgxpool.Pool, result financial.WagerResult, before string, want int) {
	t.Helper()
	rows, err := pool.Query(ctx, `SELECT event_id,event_type,aggregate_id,payload FROM outbox_events WHERE payload->>'causationId'=$1`, string(result.TransactionID))
	must(t, err)
	defer rows.Close()
	count := 0
	for rows.Next() {
		var id, kind, aggregate string
		var payload []byte
		must(t, rows.Scan(&id, &kind, &aggregate, &payload))
		var envelope struct {
			EventID, EventType, AggregateID, CorrelationID, CausationID string
			OccurredAt                                                  time.Time
			Version                                                     int
			Data                                                        json.RawMessage
		}
		must(t, json.Unmarshal(payload, &envelope))
		if envelope.EventID != id || envelope.EventType != kind || envelope.AggregateID != aggregate || envelope.CorrelationID != "correlation" || envelope.Version != 1 || envelope.OccurredAt.Location() != time.UTC {
			t.Fatalf("bad event envelope: %+v", envelope)
		}
		if kind == financial.EventWalletBalanceChanged {
			var data financial.BalanceEventData
			must(t, json.Unmarshal(envelope.Data, &data))
			if data.TransactionID != result.TransactionID || data.WalletID != result.WalletID || data.Money != result.Amount || data.BalanceBefore.Amount != before || data.BalanceAfter != result.ObservedBalance || data.WalletVersion != result.WalletVersion {
				t.Fatalf("bad balance event: %+v", data)
			}
		} else {
			expected := financial.EventWagerProcessed
			if result.State == wager.StateRejected {
				expected = financial.EventWagerRejected
			}
			if kind != expected {
				t.Fatal(kind)
			}
		}
		count++
	}
	must(t, rows.Err())
	if count != want {
		t.Fatalf("events=%d want=%d", count, want)
	}
}

func TestIntegrationPhase4MigrationUpDownUp(t *testing.T) {
	pool, ctx := phase4Pool(t)
	applyMigration(t, ctx, pool, "000003_wager_idempotency.down.sql")
	var exists bool
	must(t, pool.QueryRow(ctx, `SELECT to_regclass('wager_idempotency_records') IS NOT NULL`).Scan(&exists))
	if exists {
		t.Fatal("idempotency table survived DOWN")
	}
	countRows(t, ctx, pool, "wallets", 0)
	applyMigration(t, ctx, pool, "000003_wager_idempotency.up.sql")
	countRows(t, ctx, pool, "wager_idempotency_records", 0)
}
func TestIntegrationCreateWalletZeroAndOpening(t *testing.T) {
	pool, ctx := phase4Pool(t)
	s := financial.NewService(NewUnitOfWork(pool))
	zero := createFinancialWallet(t, ctx, s, "zero-player", 0)
	verifyWallet(t, ctx, pool, zero.WalletID, 0, 1)
	if zero.OpeningTransactionID != "" {
		t.Fatal("zero opening created")
	}
	for _, table := range []string{"wager_transactions", "wallet_ledger_entries", "outbox_events", "wager_idempotency_records"} {
		countRows(t, ctx, pool, table, 0)
	}
	positive := createFinancialWallet(t, ctx, s, "player", 10000)
	verifyWallet(t, ctx, pool, positive.WalletID, 10000, 1)
	tx, err := NewRepositories(pool).Wagers.Get(ctx, positive.OpeningTransactionID)
	must(t, err)
	if tx.Type() != wager.TypeOpening || tx.State() != wager.StateProcessed || tx.ProviderID() != "" {
		t.Fatal("invalid OPENING")
	}
	entry, err := NewRepositories(pool).Ledger.Get(ctx, string(tx.ID())+":ledger")
	must(t, err)
	if entry.BalanceBeforeCents != 0 || entry.BalanceAfterCents != 10000 || entry.WalletVersion != 1 || entry.Direction != "CREDIT" {
		t.Fatal(entry)
	}
	countRows(t, ctx, pool, "wager_transactions", 1)
	countRows(t, ctx, pool, "wallet_ledger_entries", 1)
	countRows(t, ctx, pool, "outbox_events", 2)
	verifyFinancialEvents(t, ctx, pool, financial.WagerResult{TransactionID: tx.ID(), WalletID: positive.WalletID, State: tx.State(), Amount: tx.Amount().External(), ObservedBalance: positive.Balance, WalletVersion: 1}, "0.00", 2)
	_, err = s.CreateWallet(ctx, financial.CreateWalletInput{PlayerID: "player", Currency: "BRL", InitialBalance: financialMoney(t, 0)})
	expectError(t, err, financial.ErrWalletExists)
	countRows(t, ctx, pool, "wallets", 2)
}
func TestIntegrationFinancialOutcomesAndReplay(t *testing.T) {
	pool, ctx := phase4Pool(t)
	s := financial.NewService(NewUnitOfWork(pool))
	w := createFinancialWallet(t, ctx, s, "player", 10000)
	bet := financialInput(t, w.WalletID, "player", "bet", "bet-key", wager.TypeBet, 2000)
	original, err := s.ProcessHTTPWager(ctx, bet)
	must(t, err)
	if original.State != wager.StateProcessed || original.ObservedBalance.Amount != "80.00" {
		t.Fatal(original)
	}
	verifyWallet(t, ctx, pool, w.WalletID, 8000, 2)
	verifyFinancialEvents(t, ctx, pool, original, "100.00", 2)
	entry, err := NewRepositories(pool).Ledger.Get(ctx, string(original.TransactionID)+":ledger")
	must(t, err)
	if entry.Direction != "DEBIT" || entry.AmountCents != 2000 || entry.BalanceBeforeCents != 10000 || entry.BalanceAfterCents != 8000 || entry.WalletVersion != 2 {
		t.Fatal(entry)
	}
	win, err := s.ProcessHTTPWager(ctx, financialInput(t, w.WalletID, "player", "win", "win-key", wager.TypeWin, 5000))
	must(t, err)
	verifyWallet(t, ctx, pool, w.WalletID, 13000, 3)
	verifyFinancialEvents(t, ctx, pool, win, "80.00", 2)
	entry, err = NewRepositories(pool).Ledger.Get(ctx, string(win.TransactionID)+":ledger")
	must(t, err)
	if entry.Direction != "CREDIT" || entry.BalanceBeforeCents != 8000 || entry.BalanceAfterCents != 13000 {
		t.Fatal(entry)
	}
	bet.CorrelationID = "replay-correlation"
	replay, err := s.ProcessHTTPWager(ctx, bet)
	must(t, err)
	if !replay.IdempotentReplay || replay.ObservedBalance.Amount != "80.00" {
		t.Fatal(replay)
	}
	replay.IdempotentReplay = false
	if replay != original {
		t.Fatal("replay differs from original")
	}
	bet.Amount = financialMoney(t, 2100)
	_, err = s.ProcessHTTPWager(ctx, bet)
	expectError(t, err, financial.ErrIdempotencyConflict)
	bet.Amount = financialMoney(t, 0)
	_, err = s.ProcessHTTPWager(ctx, bet)
	expectError(t, err, financial.ErrIdempotencyConflict)
	bet.Amount = financialMoney(t, 2000)
	bet.IdempotencyKey = "different-key"
	_, err = s.ProcessHTTPWager(ctx, bet)
	expectError(t, err, financial.ErrDuplicateExternalTransaction)
	_, err = NewRepositories(pool).Idempotency.Get(ctx, "different-key")
	expectError(t, err, ports.ErrNotFound)
	rejected, err := s.ProcessHTTPWager(ctx, financialInput(t, w.WalletID, "player", "rejected", "rejected-key", wager.TypeBet, 14000))
	must(t, err)
	if rejected.State != wager.StateRejected || rejected.FailureCode != financial.FailureInsufficientFunds || rejected.ObservedBalance.Amount != "130.00" {
		t.Fatal(rejected)
	}
	verifyWallet(t, ctx, pool, w.WalletID, 13000, 3)
	verifyFinancialEvents(t, ctx, pool, rejected, "130.00", 1)
	_, err = NewRepositories(pool).Ledger.Get(ctx, string(rejected.TransactionID)+":ledger")
	expectError(t, err, ports.ErrNotFound)
	loss, err := s.ProcessHTTPWager(ctx, financialInput(t, w.WalletID, "player", "loss", "loss-key", wager.TypeLoss, 0))
	must(t, err)
	if loss.State != wager.StateProcessed || loss.WalletVersion != 3 {
		t.Fatal(loss)
	}
	verifyWallet(t, ctx, pool, w.WalletID, 13000, 3)
	verifyFinancialEvents(t, ctx, pool, loss, "130.00", 1)
	countRows(t, ctx, pool, "wallet_ledger_entries", 3)
	countRows(t, ctx, pool, "wager_transactions", 5)
	countRows(t, ctx, pool, "outbox_events", 8)
	countRows(t, ctx, pool, "wager_idempotency_records", 4)
	for _, result := range []financial.WagerResult{original, win, rejected, loss} {
		persisted, err := NewRepositories(pool).Wagers.Get(ctx, result.TransactionID)
		must(t, err)
		if persisted.State() != result.State || persisted.FailureCode() != result.FailureCode {
			t.Fatal("transaction result not persisted")
		}
	}
	// Rejected replay also preserves the old observed balance after a later WIN.
	_, err = s.ProcessHTTPWager(ctx, financialInput(t, w.WalletID, "player", "later-win", "later-win-key", wager.TypeWin, 100))
	must(t, err)
	rejectionReplay, err := s.ProcessHTTPWager(ctx, financialInput(t, w.WalletID, "player", "rejected", "rejected-key", wager.TypeBet, 14000))
	must(t, err)
	if !rejectionReplay.IdempotentReplay || rejectionReplay.FailureCode != financial.FailureInsufficientFunds || rejectionReplay.ObservedBalance.Amount != "130.00" {
		t.Fatal(rejectionReplay)
	}
}

func TestIntegrationWINOverflowAndValidation(t *testing.T) {
	pool, ctx := phase4Pool(t)
	s := financial.NewService(NewUnitOfWork(pool))
	w := createFinancialWallet(t, ctx, s, "player", math.MaxInt64)
	result, err := s.ProcessHTTPWager(ctx, financialInput(t, w.WalletID, "player", "overflow", "overflow-key", wager.TypeWin, 1))
	must(t, err)
	if result.State != wager.StateRejected || result.FailureCode != financial.FailureOverflow {
		t.Fatal(result)
	}
	verifyWallet(t, ctx, pool, w.WalletID, math.MaxInt64, 1)
	verifyFinancialEvents(t, ctx, pool, result, w.Balance.Amount, 1)
	countRows(t, ctx, pool, "wallet_ledger_entries", 1)
	in := financialInput(t, w.WalletID, "other-player", "invalid", "invalid-key", wager.TypeLoss, 0)
	_, err = s.ProcessHTTPWager(ctx, in)
	expectError(t, err, financial.ErrPlayerMismatch)
	in.PlayerID = "player"
	in.Amount, _ = money.New(0, "USD")
	_, err = s.ProcessHTTPWager(ctx, in)
	expectError(t, err, financial.ErrCurrencyMismatch)
	in.Amount = financialMoney(t, 1)
	_, err = s.ProcessHTTPWager(ctx, in)
	expectError(t, err, financial.ErrInvalidAmount)
	in.Type = wager.TypeOpening
	_, err = s.ProcessHTTPWager(ctx, in)
	expectError(t, err, financial.ErrInvalidOperationType)
	countRows(t, ctx, pool, "wager_idempotency_records", 1)
}

func installOutboxFailure(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	_, err := pool.Exec(ctx, `CREATE FUNCTION test_fail_balance_event() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.event_type='WalletBalanceChanged' THEN RAISE EXCEPTION 'injected outbox failure' USING ERRCODE='23514'; END IF; RETURN NEW; END $$; CREATE TRIGGER test_outbox_failure BEFORE INSERT ON outbox_events FOR EACH ROW EXECUTE FUNCTION test_fail_balance_event();`)
	must(t, err)
}
func TestIntegrationOpeningAtomicFailure(t *testing.T) {
	pool, ctx := phase4Pool(t)
	installOutboxFailure(t, ctx, pool)
	s := financial.NewService(NewUnitOfWork(pool))
	_, err := s.CreateWallet(ctx, financial.CreateWalletInput{PlayerID: "player", Currency: "BRL", InitialBalance: financialMoney(t, 10000)})
	expectError(t, err, financial.ErrPersistence)
	for _, table := range []string{"wallets", "wager_transactions", "wallet_ledger_entries", "outbox_events"} {
		countRows(t, ctx, pool, table, 0)
	}
}
func TestIntegrationWagerAtomicFailure(t *testing.T) {
	for _, failure := range []string{"outbox", "idempotency completion"} {
		t.Run(failure, func(t *testing.T) {
			pool, ctx := phase4Pool(t)
			s := financial.NewService(NewUnitOfWork(pool))
			w := createFinancialWallet(t, ctx, s, "player", 10000)
			if failure == "outbox" {
				installOutboxFailure(t, ctx, pool)
			} else {
				_, err := pool.Exec(ctx, `CREATE FUNCTION test_fail_completion() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected completion failure' USING ERRCODE='23514'; END $$; CREATE TRIGGER test_completion_failure BEFORE UPDATE ON wager_idempotency_records FOR EACH ROW EXECUTE FUNCTION test_fail_completion();`)
				must(t, err)
			}
			_, err := s.ProcessHTTPWager(ctx, financialInput(t, w.WalletID, "player", "bet", "key", wager.TypeBet, 2000))
			expectError(t, err, financial.ErrPersistence)
			verifyWallet(t, ctx, pool, w.WalletID, 10000, 1)
			countRows(t, ctx, pool, "wager_transactions", 1)
			countRows(t, ctx, pool, "wallet_ledger_entries", 1)
			countRows(t, ctx, pool, "outbox_events", 2)
			countRows(t, ctx, pool, "wager_idempotency_records", 0)
			if failure == "outbox" {
				_, err = pool.Exec(ctx, `DROP TRIGGER test_outbox_failure ON outbox_events`)
			} else {
				_, err = pool.Exec(ctx, `DROP TRIGGER test_completion_failure ON wager_idempotency_records`)
			}
			must(t, err)
			result, err := s.ProcessHTTPWager(ctx, financialInput(t, w.WalletID, "player", "bet", "key", wager.TypeBet, 2000))
			must(t, err)
			if result.IdempotentReplay {
				t.Fatal("rolled back claim survived")
			}
			verifyWallet(t, ctx, pool, w.WalletID, 8000, 2)
		})
	}
}

type concurrentResult struct {
	result financial.WagerResult
	err    error
}

func concurrentOperations(ctx context.Context, services []*financial.Service, inputs []financial.HTTPWagerInput) []concurrentResult {
	start := make(chan struct{})
	results := make(chan concurrentResult, len(inputs))
	var ready sync.WaitGroup
	ready.Add(len(inputs))
	for i, in := range inputs {
		go func(s *financial.Service, in financial.HTTPWagerInput) {
			ready.Done()
			<-start
			result, err := s.ProcessHTTPWager(ctx, in)
			results <- concurrentResult{result, err}
		}(services[i%len(services)], in)
	}
	ready.Wait()
	close(start)
	out := make([]concurrentResult, 0, len(inputs))
	for range inputs {
		out = append(out, <-results)
	}
	return out
}
func secondService(t *testing.T, ctx context.Context, pool *pgxpool.Pool) *financial.Service {
	t.Helper()
	other, err := pgxpool.NewWithConfig(ctx, pool.Config())
	must(t, err)
	t.Cleanup(other.Close)
	return financial.NewService(NewUnitOfWork(other))
}
func TestIntegrationTwoBetsAgainst100(t *testing.T) {
	pool, ctx := phase4Pool(t)
	s := financial.NewService(NewUnitOfWork(pool))
	w := createFinancialWallet(t, ctx, s, "player", 10000)
	services := []*financial.Service{s, secondService(t, ctx, pool)}
	inputs := []financial.HTTPWagerInput{financialInput(t, w.WalletID, "player", "bet-a", "key-a", wager.TypeBet, 8000), financialInput(t, w.WalletID, "player", "bet-b", "key-b", wager.TypeBet, 8000)}
	outcomes := concurrentOperations(ctx, services, inputs)
	processed, rejected := 0, 0
	for _, out := range outcomes {
		must(t, out.err)
		switch out.result.State {
		case wager.StateProcessed:
			processed++
		case wager.StateRejected:
			rejected++
			if out.result.FailureCode != financial.FailureInsufficientFunds {
				t.Fatal(out.result)
			}
		default:
			t.Fatal(out.result)
		}
	}
	if processed != 1 || rejected != 1 {
		t.Fatalf("processed=%d rejected=%d", processed, rejected)
	}
	verifyWallet(t, ctx, pool, w.WalletID, 2000, 2)
	var debits int
	must(t, pool.QueryRow(ctx, `SELECT count(*) FROM wallet_ledger_entries WHERE direction='DEBIT'`).Scan(&debits))
	if debits != 1 {
		t.Fatal(debits)
	}
	countRows(t, ctx, pool, "wager_transactions", 3)
	countRows(t, ctx, pool, "outbox_events", 5)
	countRows(t, ctx, pool, "wager_idempotency_records", 2)
	t.Log("100.00 vs 2x80.00: 1 PROCESSED, 1 INSUFFICIENT_FUNDS, balance 20.00, version 2, 1 DEBIT")
}
func TestIntegrationFiftyConcurrentReplays(t *testing.T) {
	pool, ctx := phase4Pool(t)
	s := financial.NewService(NewUnitOfWork(pool))
	w := createFinancialWallet(t, ctx, s, "player", 10000)
	services := []*financial.Service{s, secondService(t, ctx, pool)}
	in := financialInput(t, w.WalletID, "player", "bet", "key", wager.TypeBet, 2000)
	inputs := make([]financial.HTTPWagerInput, 50)
	for i := range inputs {
		inputs[i] = in
		inputs[i].CorrelationID = fmt.Sprintf("attempt-%d", i)
	}
	outcomes := concurrentOperations(ctx, services, inputs)
	first, replays := 0, 0
	var original financial.WagerResult
	for _, out := range outcomes {
		must(t, out.err)
		if out.result.IdempotentReplay {
			replays++
		} else {
			first++
			original = out.result
		}
	}
	if first != 1 || replays != 49 {
		t.Fatalf("first=%d replay=%d", first, replays)
	}
	for _, out := range outcomes {
		out.result.IdempotentReplay = false
		if out.result != original {
			t.Fatal("inconsistent replay")
		}
	}
	verifyWallet(t, ctx, pool, w.WalletID, 8000, 2)
	countRows(t, ctx, pool, "wallet_ledger_entries", 2)
	countRows(t, ctx, pool, "wager_transactions", 2)
	countRows(t, ctx, pool, "outbox_events", 4)
	countRows(t, ctx, pool, "wager_idempotency_records", 1)
	t.Log("50 concurrent attempts across independent pools: 1 execution, 49 replays, 1 operation ledger, balance 80.00")
}
func TestIntegrationConcurrentExternalDuplicateDifferentKeys(t *testing.T) {
	pool, ctx := phase4Pool(t)
	s := financial.NewService(NewUnitOfWork(pool))
	w := createFinancialWallet(t, ctx, s, "player", 10000)
	a := financialInput(t, w.WalletID, "player", "same-external", "key-a", wager.TypeBet, 2000)
	b := a
	b.IdempotencyKey = "key-b"
	outcomes := concurrentOperations(ctx, []*financial.Service{s, secondService(t, ctx, pool)}, []financial.HTTPWagerInput{a, b})
	success, conflict := 0, 0
	for _, out := range outcomes {
		if out.err == nil {
			success++
		} else if errors.Is(out.err, financial.ErrDuplicateExternalTransaction) {
			conflict++
		} else {
			t.Fatal(out.err)
		}
	}
	if success != 1 || conflict != 1 {
		t.Fatalf("success=%d conflict=%d", success, conflict)
	}
	verifyWallet(t, ctx, pool, w.WalletID, 8000, 2)
	countRows(t, ctx, pool, "wallet_ledger_entries", 2)
	countRows(t, ctx, pool, "wager_idempotency_records", 1)
}
func TestIntegrationIndependentWalletsProgress(t *testing.T) {
	pool, ctx := phase4Pool(t)
	s := financial.NewService(NewUnitOfWork(pool))
	otherService := secondService(t, ctx, pool)
	a := createFinancialWallet(t, ctx, s, "player-a", 10000)
	b := createFinancialWallet(t, ctx, s, "player-b", 10000)
	// Force A to wait in PostgreSQL while B must complete its whole financial use case.
	holder, err := pool.Begin(ctx)
	must(t, err)
	defer holder.Rollback(context.Background())
	_, err = repositories(holder, true).Wallets.GetForUpdate(ctx, a.WalletID)
	must(t, err)
	var holderPID int32
	must(t, holder.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&holderPID))
	inA := financialInput(t, a.WalletID, "player-a", "bet-a", "key-a", wager.TypeBet, 8000)
	inB := financialInput(t, b.WalletID, "player-b", "bet-b", "key-b", wager.TypeBet, 8000)
	done := make(chan concurrentResult, 1)
	go func() { result, err := s.ProcessHTTPWager(ctx, inA); done <- concurrentResult{result, err} }()
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		var waiting bool
		must(t, pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE $1=ANY(pg_blocking_pids(pid)))`, holderPID).Scan(&waiting))
		if waiting {
			break
		}
		select {
		case out := <-done:
			t.Fatalf("A did not wait: %+v", out)
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-ticker.C:
		}
	}
	otherCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	resultB, err := otherService.ProcessHTTPWager(otherCtx, inB)
	must(t, err)
	if resultB.State != wager.StateProcessed {
		t.Fatal(resultB)
	}
	select {
	case out := <-done:
		t.Fatalf("A unexpectedly completed: %+v", out)
	default:
	}
	must(t, holder.Commit(ctx))
	select {
	case out := <-done:
		must(t, out.err)
		if out.result.State != wager.StateProcessed {
			t.Fatal(out.result)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	verifyWallet(t, ctx, pool, a.WalletID, 2000, 2)
	verifyWallet(t, ctx, pool, b.WalletID, 2000, 2)
	countRows(t, ctx, pool, "wallet_ledger_entries", 4)
	countRows(t, ctx, pool, "outbox_events", 8)
	t.Log("wallet B committed while A was blocked: no global financial lock")
}
