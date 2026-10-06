package postgres

import (
	"context"
	"fmt"
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

func phase5Pool(t *testing.T) (*pgxpool.Pool, context.Context) {
	t.Helper()
	pool, ctx := phase4Pool(t)
	applyMigration(t, ctx, pool, "000004_reversals_pending_references.up.sql")
	return pool, ctx
}
func referenceInput(t *testing.T, id wallet.ID, player, external, key string, kind wager.Type, amount int64, reference string) financial.HTTPWagerInput {
	t.Helper()
	in := financialInput(t, id, player, external, key, kind, amount)
	in.ReferenceExternalTransactionID = wager.ExternalTransactionID(reference)
	return in
}
func runFinancial(t *testing.T, ctx context.Context, s *financial.Service, in financial.HTTPWagerInput) financial.WagerResult {
	t.Helper()
	result, err := s.ProcessHTTPWager(ctx, in)
	must(t, err)
	return result
}
func readPending(t *testing.T, ctx context.Context, pool *pgxpool.Pool, id wager.TransactionID) ports.PendingReference {
	t.Helper()
	p := ports.PendingReference{TransactionID: id}
	must(t, pool.QueryRow(ctx, `SELECT correlation_id,attempt_count,max_attempts,first_pending_at,last_attempt_at,next_attempt_at,expires_at,completed_at FROM pending_wager_references WHERE transaction_id=$1`, string(id)).Scan(&p.CorrelationID, &p.AttemptCount, &p.MaxAttempts, &p.FirstPendingAt, &p.LastAttemptAt, &p.NextAttemptAt, &p.ExpiresAt, &p.CompletedAt))
	return p
}
func TestIntegrationPhase5MigrationUpDownUp(t *testing.T) {
	pool, ctx := phase5Pool(t)
	applyMigration(t, ctx, pool, "000004_reversals_pending_references.down.sql")
	for _, name := range []string{"wager_reversals", "pending_wager_references"} {
		var exists bool
		must(t, pool.QueryRow(ctx, `SELECT to_regclass($1) IS NOT NULL`, name).Scan(&exists))
		if exists {
			t.Fatal("table survived DOWN")
		}
	}
	countRows(t, ctx, pool, "wallets", 0)
	countRows(t, ctx, pool, "wager_idempotency_records", 0)
	applyMigration(t, ctx, pool, "000004_reversals_pending_references.up.sql")
	countRows(t, ctx, pool, "wager_reversals", 0)
	countRows(t, ctx, pool, "pending_wager_references", 0)
}
func TestIntegrationRefundAndRollbackPaths(t *testing.T) {
	for _, tc := range []struct {
		name              string
		original, reverse wager.Type
		amount, after     int64
		direction         ports.LedgerDirection
	}{
		{"refund-bet", wager.TypeBet, wager.TypeRefund, 2000, 10000, ports.Credit},
		{"rollback-bet", wager.TypeBet, wager.TypeRollback, 2000, 10000, ports.Credit},
		{"rollback-win", wager.TypeWin, wager.TypeRollback, 5000, 10000, ports.Debit},
		{"rollback-refund", wager.TypeRefund, wager.TypeRollback, 2000, 8000, ports.Debit},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pool, ctx := phase5Pool(t)
			s := financial.NewService(NewUnitOfWork(pool))
			w := createFinancialWallet(t, ctx, s, "player", 10000)
			originalInput := financialInput(t, w.WalletID, "player", "original", "original-key", tc.original, tc.amount)
			if tc.original == wager.TypeRefund {
				runFinancial(t, ctx, s, financialInput(t, w.WalletID, "player", "bet", "bet-key", wager.TypeBet, 2000))
				originalInput.ReferenceExternalTransactionID = "bet"
			}
			original := runFinancial(t, ctx, s, originalInput)
			before, err := NewRepositories(pool).Wallets.Get(ctx, w.WalletID)
			must(t, err)
			in := referenceInput(t, w.WalletID, "player", "reverse", "reverse-key", tc.reverse, tc.amount, "original")
			result := runFinancial(t, ctx, s, in)
			if result.State != wager.StateProcessed {
				t.Fatal(result)
			}
			verifyWallet(t, ctx, pool, w.WalletID, tc.after, original.WalletVersion+1)
			entry, err := NewRepositories(pool).Ledger.Get(ctx, string(result.TransactionID)+":ledger")
			must(t, err)
			if entry.Direction != tc.direction || entry.BalanceBeforeCents != before.Balance().Cents() || entry.BalanceAfterCents != tc.after || entry.AmountCents != tc.amount {
				t.Fatal(entry)
			}
			verifyFinancialEvents(t, ctx, pool, result, before.Balance().DecimalString(), 2)
			claim, err := NewRepositories(pool).Reversals.Get(ctx, original.TransactionID)
			must(t, err)
			if claim.ReversalTransactionID != result.TransactionID {
				t.Fatal(claim)
			}
			replay := runFinancial(t, ctx, s, in)
			if !replay.IdempotentReplay {
				t.Fatal("reapplied reverse")
			}
			replay.IdempotentReplay = false
			if replay != result {
				t.Fatal("replay changed result")
			}
			in.ExternalTransactionID = "duplicate"
			in.IdempotencyKey = "duplicate-key"
			duplicate := runFinancial(t, ctx, s, in)
			if duplicate.State != wager.StateRejected || duplicate.FailureCode != financial.FailureAlreadyReversed {
				t.Fatal(duplicate)
			}
			verifyWallet(t, ctx, pool, w.WalletID, tc.after, original.WalletVersion+1)
			_, err = NewRepositories(pool).Ledger.Get(ctx, string(duplicate.TransactionID)+":ledger")
			expectError(t, err, ports.ErrNotFound)
			verifyFinancialEvents(t, ctx, pool, duplicate, result.ObservedBalance.Amount, 1)
		})
	}
}
func TestIntegrationInsufficientFundsForReversal(t *testing.T) {
	pool, ctx := phase5Pool(t)
	s := financial.NewService(NewUnitOfWork(pool))
	w := createFinancialWallet(t, ctx, s, "player", 10000)
	runFinancial(t, ctx, s, financialInput(t, w.WalletID, "player", "win", "win-key", wager.TypeWin, 5000))
	runFinancial(t, ctx, s, financialInput(t, w.WalletID, "player", "bet", "bet-key", wager.TypeBet, 13000))
	in := referenceInput(t, w.WalletID, "player", "rollback", "rollback-key", wager.TypeRollback, 5000, "win")
	result := runFinancial(t, ctx, s, in)
	if result.FailureCode != financial.FailureInsufficientFundsForReversal || result.State != wager.StateRejected {
		t.Fatal(result)
	}
	verifyWallet(t, ctx, pool, w.WalletID, 2000, 3)
	countRows(t, ctx, pool, "wallet_ledger_entries", 3)
	countRows(t, ctx, pool, "wager_reversals", 0)
	verifyFinancialEvents(t, ctx, pool, result, "20.00", 1)
	runFinancial(t, ctx, s, financialInput(t, w.WalletID, "player", "credit", "credit-key", wager.TypeWin, 5000))
	in.ExternalTransactionID = "retry-reversal"
	in.IdempotencyKey = "retry-reversal-key"
	result = runFinancial(t, ctx, s, in)
	if result.State != wager.StateProcessed {
		t.Fatal("failed reversal consumed claim")
	}
	verifyWallet(t, ctx, pool, w.WalletID, 2000, 5)
	countRows(t, ctx, pool, "wager_reversals", 1)
}
func TestIntegrationReferenceValidationAndProviderScope(t *testing.T) {
	for _, mode := range []string{"amount", "round", "player", "wallet", "invalid-type", "invalid-state", "provider-scope"} {
		t.Run(mode, func(t *testing.T) {
			pool, ctx := phase5Pool(t)
			s := financial.NewService(NewUnitOfWork(pool))
			w := createFinancialWallet(t, ctx, s, "player", 10000)
			original := financialInput(t, w.WalletID, "player", "original", "original-key", wager.TypeBet, 2000)
			want := financial.FailureReferenceAmountMismatch
			in := referenceInput(t, w.WalletID, "player", "refund", "refund-key", wager.TypeRefund, 2000, "original")
			switch mode {
			case "amount":
				in.Amount = financialMoney(t, 2100)
			case "round":
				original.RoundID = "another-round"
				want = financial.FailureReferenceRoundMismatch
			case "player":
				other := createFinancialWallet(t, ctx, s, "other-player", 10000)
				original.WalletID = other.WalletID
				original.PlayerID = "other-player"
				want = financial.FailureReferencePlayerMismatch
			case "wallet":
				other := createFinancialWalletUSD(t, ctx, s, "player")
				original.WalletID = other
				original.Amount, _ = money.New(2000, "USD")
				want = financial.FailureReferenceWalletMismatch
			case "invalid-type":
				original.Type = wager.TypeWin
				want = financial.FailureReferenceTypeInvalid
			case "invalid-state":
				original.Amount = financialMoney(t, 20000)
				want = financial.FailureReferenceStateInvalid
			case "provider-scope":
				original.ProviderID = "another-provider"
			}
			runFinancial(t, ctx, s, original)
			result := runFinancial(t, ctx, s, in)
			if mode == "provider-scope" {
				if result.State != wager.StatePendingReference {
					t.Fatal("looked up external ID globally")
				}
				return
			}
			if result.State != wager.StateRejected || result.FailureCode != want {
				t.Fatalf("result=%+v want=%s", result, want)
			}
			countRows(t, ctx, pool, "wager_reversals", 0)
			_, err := NewRepositories(pool).Ledger.Get(ctx, string(result.TransactionID)+":ledger")
			expectError(t, err, ports.ErrNotFound)
		})
	}
}
func createFinancialWalletUSD(t *testing.T, ctx context.Context, s *financial.Service, player string) wallet.ID {
	t.Helper()
	balance, err := money.New(10000, "USD")
	must(t, err)
	result, err := s.CreateWallet(ctx, financial.CreateWalletInput{PlayerID: wallet.PlayerID(player), Currency: "USD", InitialBalance: balance})
	must(t, err)
	return result.WalletID
}

func TestIntegrationConcurrentReversals(t *testing.T) {
	for _, first := range []wager.Type{wager.TypeRefund, wager.TypeRollback} {
		for _, second := range []wager.Type{wager.TypeRefund, wager.TypeRollback} {
			t.Run(string(first)+"/"+string(second), func(t *testing.T) {
				pool, ctx := phase5Pool(t)
				s := financial.NewService(NewUnitOfWork(pool))
				w := createFinancialWallet(t, ctx, s, "player", 10000)
				original := runFinancial(t, ctx, s, financialInput(t, w.WalletID, "player", "bet", "bet-key", wager.TypeBet, 2000))
				a := referenceInput(t, w.WalletID, "player", "reverse-a", "key-a", first, 2000, "bet")
				b := referenceInput(t, w.WalletID, "player", "reverse-b", "key-b", second, 2000, "bet")
				outcomes := concurrentOperations(ctx, []*financial.Service{s, secondService(t, ctx, pool)}, []financial.HTTPWagerInput{a, b})
				processed, rejected := 0, 0
				for _, out := range outcomes {
					must(t, out.err)
					if out.result.State == wager.StateProcessed {
						processed++
					} else if out.result.FailureCode == financial.FailureAlreadyReversed {
						rejected++
					} else {
						t.Fatal(out.result)
					}
				}
				if processed != 1 || rejected != 1 {
					t.Fatalf("processed=%d rejected=%d", processed, rejected)
				}
				verifyWallet(t, ctx, pool, w.WalletID, 10000, 3)
				countRows(t, ctx, pool, "wager_reversals", 1)
				countRows(t, ctx, pool, "wallet_ledger_entries", 3)
				// The unique original key also protects callers bypassing application checks.
				rev, err := NewRepositories(pool).Reversals.Get(ctx, original.TransactionID)
				must(t, err)
				claimed := true
				must(t, NewUnitOfWork(pool).WithinTransaction(ctx, func(r ports.Repositories) error {
					var err error
					claimed, err = r.Reversals.Claim(ctx, rev)
					return err
				}))
				if claimed {
					t.Fatal("duplicate reversal reservation")
				}
				t.Log("independent pools: one successful reversal, one ALREADY_REVERSED, no duplicate credit")
			})
		}
	}
}
func TestIntegrationReversalConcurrentWithOtherChanges(t *testing.T) {
	pool, ctx := phase5Pool(t)
	s := financial.NewService(NewUnitOfWork(pool))
	w := createFinancialWallet(t, ctx, s, "player", 10000)
	runFinancial(t, ctx, s, financialInput(t, w.WalletID, "player", "bet", "bet-key", wager.TypeBet, 2000))
	inputs := []financial.HTTPWagerInput{
		referenceInput(t, w.WalletID, "player", "refund", "refund-key", wager.TypeRefund, 2000, "bet"),
		financialInput(t, w.WalletID, "player", "win", "win-key", wager.TypeWin, 5000),
		financialInput(t, w.WalletID, "player", "another-bet", "another-key", wager.TypeBet, 3000),
	}
	for _, out := range concurrentOperations(ctx, []*financial.Service{s, secondService(t, ctx, pool)}, inputs) {
		must(t, out.err)
		if out.result.State != wager.StateProcessed {
			t.Fatal(out.result)
		}
	}
	verifyWallet(t, ctx, pool, w.WalletID, 12000, 5)
	countRows(t, ctx, pool, "wallet_ledger_entries", 5)
	// Replay every operation keeps its own observed result despite concurrent ordering.
	for _, in := range inputs {
		result := runFinancial(t, ctx, s, in)
		if !result.IdempotentReplay {
			t.Fatal("concurrent operation lost idempotency")
		}
	}
}

func TestIntegrationPendingOutOfOrderRecoveryAndIdempotency(t *testing.T) {
	for _, reverse := range []wager.Type{wager.TypeRefund, wager.TypeRollback} {
		t.Run(string(reverse), func(t *testing.T) {
			pool, ctx := phase5Pool(t)
			s := financial.NewService(NewUnitOfWork(pool))
			w := createFinancialWallet(t, ctx, s, "player", 10000)
			in := referenceInput(t, w.WalletID, "player", "reverse", "reverse-key", reverse, 2000, "late-bet")
			pending := runFinancial(t, ctx, s, in)
			if pending.State != wager.StatePendingReference {
				t.Fatal(pending)
			}
			verifyWallet(t, ctx, pool, w.WalletID, 10000, 1)
			countRows(t, ctx, pool, "wallet_ledger_entries", 1)
			countRows(t, ctx, pool, "wager_reversals", 0)
			var pendingEvents int
			must(t, pool.QueryRow(ctx, `SELECT count(*) FROM outbox_events WHERE event_type='WagerTransactionPendingReference'`).Scan(&pendingEvents))
			if pendingEvents != 1 {
				t.Fatal(pendingEvents)
			}
			replay := runFinancial(t, ctx, s, in)
			if !replay.IdempotentReplay || replay.State != wager.StatePendingReference {
				t.Fatal(replay)
			}
			changed := in
			changed.ReferenceExternalTransactionID = "different-reference"
			_, err := s.ProcessHTTPWager(ctx, changed)
			expectError(t, err, financial.ErrIdempotencyConflict)
			changed = in
			changed.IdempotencyKey = "different-key"
			_, err = s.ProcessHTTPWager(ctx, changed)
			expectError(t, err, financial.ErrDuplicateExternalTransaction)
			p := readPending(t, ctx, pool, pending.TransactionID)
			if p.AttemptCount != 0 || p.LastAttemptAt != nil || p.CompletedAt != nil {
				t.Fatal(p)
			}
			handled, err := s.ResolvePendingOnce(ctx, p.NextAttemptAt)
			must(t, err)
			if !handled {
				t.Fatal("retry did not find durable item")
			}
			p = readPending(t, ctx, pool, p.TransactionID)
			if p.AttemptCount != 1 || p.LastAttemptAt == nil {
				t.Fatal(p)
			}
			countRows(t, ctx, pool, "outbox_events", 3)
			// A fresh pool/service recovers all retry state from PostgreSQL.
			recovered := secondService(t, ctx, pool)
			runFinancial(t, ctx, recovered, financialInput(t, w.WalletID, "player", "late-bet", "late-key", wager.TypeBet, 2000))
			handled, err = recovered.ResolvePendingOnce(ctx, p.NextAttemptAt)
			must(t, err)
			if !handled {
				t.Fatal("recovery failed")
			}
			verifyWallet(t, ctx, pool, w.WalletID, 10000, 3)
			countRows(t, ctx, pool, "wallet_ledger_entries", 3)
			countRows(t, ctx, pool, "wager_reversals", 1)
			countRows(t, ctx, pool, "outbox_events", 7)
			p = readPending(t, ctx, pool, p.TransactionID)
			if p.CompletedAt == nil || p.AttemptCount != 2 {
				t.Fatal(p)
			}
			terminal := runFinancial(t, ctx, recovered, in)
			if !terminal.IdempotentReplay || terminal.State != wager.StateProcessed || terminal.TransactionID != pending.TransactionID || terminal.ObservedBalance.Amount != "100.00" || terminal.WalletVersion != 3 {
				t.Fatal(terminal)
			}
			entry, err := NewRepositories(pool).Ledger.Get(ctx, string(pending.TransactionID)+":ledger")
			must(t, err)
			if entry.Direction != ports.Credit || entry.BalanceBeforeCents != 8000 || entry.BalanceAfterCents != 10000 {
				t.Fatal(entry)
			}
			verifyFinancialEventsAllowPending(t, ctx, pool, pending.TransactionID)
			runFinancial(t, ctx, recovered, financialInput(t, w.WalletID, "player", "later-win", "win-key", wager.TypeWin, 5000))
			if replay = runFinancial(t, ctx, recovered, in); replay.ObservedBalance.Amount != "100.00" {
				t.Fatal("terminal observed balance drifted")
			}
			t.Log("out-of-order and new-instance recovery: pending -> PROCESSED, replay updated atomically")
		})
	}
}
func verifyFinancialEventsAllowPending(t *testing.T, ctx context.Context, pool *pgxpool.Pool, id wager.TransactionID) {
	t.Helper()
	for _, kind := range []string{financial.EventWagerPendingReference, financial.EventWagerProcessed, financial.EventWalletBalanceChanged} {
		var count int
		must(t, pool.QueryRow(ctx, `SELECT count(*) FROM outbox_events WHERE payload->>'causationId'=$1 AND event_type=$2`, string(id), kind).Scan(&count))
		if count != 1 {
			t.Fatalf("%s count=%d", kind, count)
		}
	}
}
func TestIntegrationPendingExpirationAndInvalidReference(t *testing.T) {
	for _, mode := range []string{"max-attempts", "TTL", "invalid-found"} {
		t.Run(mode, func(t *testing.T) {
			pool, ctx := phase5Pool(t)
			policy := financial.DefaultPendingPolicy()
			policy.MaxAttempts = 1
			s, err := financial.NewServiceWithPolicy(NewUnitOfWork(pool), policy)
			must(t, err)
			w := createFinancialWallet(t, ctx, s, "player", 10000)
			in := referenceInput(t, w.WalletID, "player", "refund", "refund-key", wager.TypeRefund, 2000, "late")
			result := runFinancial(t, ctx, s, in)
			p := readPending(t, ctx, pool, result.TransactionID)
			at := p.NextAttemptAt
			code := financial.FailureReferenceNotFound
			if mode == "TTL" {
				at = p.ExpiresAt
			} else if mode == "invalid-found" {
				runFinancial(t, ctx, s, financialInput(t, w.WalletID, "player", "late", "late-key", wager.TypeWin, 2000))
				code = financial.FailureReferenceTypeInvalid
			}
			walletBefore, err := NewRepositories(pool).Wallets.Get(ctx, w.WalletID)
			must(t, err)
			handled, err := s.ResolvePendingOnce(ctx, at)
			must(t, err)
			if !handled {
				t.Fatal("did not expire")
			}
			replay := runFinancial(t, ctx, s, in)
			if replay.State != wager.StateRejected || replay.FailureCode != code || !replay.IdempotentReplay {
				t.Fatal(replay)
			}
			verifyWallet(t, ctx, pool, w.WalletID, walletBefore.Balance().Cents(), walletBefore.Version())
			p = readPending(t, ctx, pool, p.TransactionID)
			if p.CompletedAt == nil {
				t.Fatal("not terminal")
			}
			_, err = NewRepositories(pool).Ledger.Get(ctx, string(result.TransactionID)+":ledger")
			expectError(t, err, ports.ErrNotFound)
			var rejectedEvents int
			must(t, pool.QueryRow(ctx, `SELECT count(*) FROM outbox_events WHERE event_type='WagerTransactionRejected' AND payload->>'causationId'=$1`, string(result.TransactionID)).Scan(&rejectedEvents))
			if rejectedEvents != 1 {
				t.Fatal(rejectedEvents)
			}
			handled, err = s.ResolvePendingOnce(ctx, at.Add(time.Hour))
			must(t, err)
			if handled {
				t.Fatal("completed pending reclaimed")
			}
		})
	}
}

func TestIntegrationPendingWorkersAndSkipLocked(t *testing.T) {
	pool, ctx := phase5Pool(t)
	s := financial.NewService(NewUnitOfWork(pool))
	other := secondService(t, ctx, pool)
	w := createFinancialWallet(t, ctx, s, "player", 10000)
	a := runFinancial(t, ctx, s, referenceInput(t, w.WalletID, "player", "refund-a", "key-a", wager.TypeRefund, 2000, "late-a"))
	b := runFinancial(t, ctx, s, referenceInput(t, w.WalletID, "player", "refund-b", "key-b", wager.TypeRefund, 2000, "late-b"))
	runFinancial(t, ctx, s, financialInput(t, w.WalletID, "player", "late-a", "late-a-key", wager.TypeBet, 2000))
	runFinancial(t, ctx, s, financialInput(t, w.WalletID, "player", "late-b", "late-b-key", wager.TypeBet, 2000))
	at := readPending(t, ctx, pool, b.TransactionID).NextAttemptAt.Add(time.Second)
	holder, err := pool.Begin(ctx)
	must(t, err)
	defer holder.Rollback(context.Background())
	held, err := repositories(holder, true).PendingReferences.ClaimNext(ctx, at)
	must(t, err)
	short, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	handled, err := other.ResolvePendingOnce(short, at)
	must(t, err)
	if !handled {
		t.Fatal("locked head prevented progress on next item")
	}
	var completed int
	must(t, pool.QueryRow(ctx, `SELECT count(*) FROM pending_wager_references WHERE completed_at IS NOT NULL`).Scan(&completed))
	if completed != 1 {
		t.Fatal(completed)
	}
	tx, err := NewRepositories(pool).Wagers.Get(ctx, held.TransactionID)
	must(t, err)
	if tx.State() != wager.StatePendingReference {
		t.Fatal("claimed locked item")
	}
	handled, err = other.ResolvePendingOnce(short, at)
	must(t, err)
	if handled {
		t.Fatal("claimed held item")
	}
	must(t, holder.Rollback(ctx))
	// Two independent workers compete for the last remaining item.
	start := make(chan struct{})
	out := make(chan struct {
		handled bool
		err     error
	}, 2)
	var ready sync.WaitGroup
	ready.Add(2)
	for _, service := range []*financial.Service{s, other} {
		go func(service *financial.Service) {
			ready.Done()
			<-start
			h, err := service.ResolvePendingOnce(ctx, at)
			out <- struct {
				handled bool
				err     error
			}{h, err}
		}(service)
	}
	ready.Wait()
	close(start)
	handledCount := 0
	for i := 0; i < 2; i++ {
		r := <-out
		must(t, r.err)
		if r.handled {
			handledCount++
		}
	}
	if handledCount != 1 {
		t.Fatal("same pending processed twice")
	}
	verifyWallet(t, ctx, pool, w.WalletID, 10000, 5)
	countRows(t, ctx, pool, "wager_reversals", 2)
	countRows(t, ctx, pool, "wallet_ledger_entries", 5)
	for _, id := range []wager.TransactionID{a.TransactionID, b.TransactionID} {
		verifyFinancialEventsAllowPending(t, ctx, pool, id)
	}
	t.Log("SKIP LOCKED progressed past held row; two workers resolved remaining row exactly once")
}

func TestIntegrationReversalAndPendingAtomicFailure(t *testing.T) {
	for _, pending := range []bool{false, true} {
		t.Run(fmt.Sprint(pending), func(t *testing.T) {
			pool, ctx := phase5Pool(t)
			s := financial.NewService(NewUnitOfWork(pool))
			w := createFinancialWallet(t, ctx, s, "player", 10000)
			in := referenceInput(t, w.WalletID, "player", "refund", "refund-key", wager.TypeRefund, 2000, "bet")
			var result financial.WagerResult
			var at time.Time
			if pending {
				result = runFinancial(t, ctx, s, in)
				at = readPending(t, ctx, pool, result.TransactionID).NextAttemptAt
			}
			runFinancial(t, ctx, s, financialInput(t, w.WalletID, "player", "bet", "bet-key", wager.TypeBet, 2000))
			installOutboxFailure(t, ctx, pool)
			if pending {
				_, err := s.ResolvePendingOnce(ctx, at)
				expectError(t, err, financial.ErrPersistence)
				tx, err := NewRepositories(pool).Wagers.Get(ctx, result.TransactionID)
				must(t, err)
				if tx.State() != wager.StatePendingReference {
					t.Fatal(tx.State())
				}
				p := readPending(t, ctx, pool, result.TransactionID)
				if p.AttemptCount != 0 || p.CompletedAt != nil {
					t.Fatal("retry metadata partially committed")
				}
				replay := runFinancial(t, ctx, s, in)
				if replay.State != wager.StatePendingReference {
					t.Fatal(replay)
				}
			} else {
				_, err := s.ProcessHTTPWager(ctx, in)
				expectError(t, err, financial.ErrPersistence)
				_, err = NewRepositories(pool).Idempotency.Get(ctx, "refund-key")
				expectError(t, err, ports.ErrNotFound)
			}
			verifyWallet(t, ctx, pool, w.WalletID, 8000, 2)
			countRows(t, ctx, pool, "wallet_ledger_entries", 2)
			countRows(t, ctx, pool, "wager_reversals", 0)
			_, err := pool.Exec(ctx, `DROP TRIGGER test_outbox_failure ON outbox_events`)
			must(t, err)
			if pending {
				handled, err := s.ResolvePendingOnce(ctx, at)
				must(t, err)
				if !handled {
					t.Fatal("rolled back pending lost")
				}
			} else {
				result = runFinancial(t, ctx, s, in)
				if result.State != wager.StateProcessed {
					t.Fatal(result)
				}
			}
			verifyWallet(t, ctx, pool, w.WalletID, 10000, 3)
			countRows(t, ctx, pool, "wager_reversals", 1)
		})
	}
}

func TestIntegrationReversalWalletsRemainIndependent(t *testing.T) {
	pool, ctx := phase5Pool(t)
	s := financial.NewService(NewUnitOfWork(pool))
	other := secondService(t, ctx, pool)
	a := createFinancialWallet(t, ctx, s, "player-a", 10000)
	b := createFinancialWallet(t, ctx, s, "player-b", 10000)
	runFinancial(t, ctx, s, financialInput(t, a.WalletID, "player-a", "bet-a", "bet-a-key", wager.TypeBet, 2000))
	runFinancial(t, ctx, s, financialInput(t, b.WalletID, "player-b", "bet-b", "bet-b-key", wager.TypeBet, 2000))
	holder, err := pool.Begin(ctx)
	must(t, err)
	defer holder.Rollback(context.Background())
	_, err = repositories(holder, true).Wallets.GetForUpdate(ctx, a.WalletID)
	must(t, err)
	var pid int32
	must(t, holder.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&pid))
	inA := referenceInput(t, a.WalletID, "player-a", "refund-a", "refund-a-key", wager.TypeRefund, 2000, "bet-a")
	done := make(chan concurrentResult, 1)
	go func() { result, err := s.ProcessHTTPWager(ctx, inA); done <- concurrentResult{result, err} }()
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		var waiting bool
		must(t, pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE $1=ANY(pg_blocking_pids(pid)))`, pid).Scan(&waiting))
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
	short, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	resultB := runFinancial(t, short, other, referenceInput(t, b.WalletID, "player-b", "refund-b", "refund-b-key", wager.TypeRefund, 2000, "bet-b"))
	if resultB.State != wager.StateProcessed {
		t.Fatal(resultB)
	}
	must(t, holder.Commit(ctx))
	select {
	case out := <-done:
		must(t, out.err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	verifyWallet(t, ctx, pool, a.WalletID, 10000, 3)
	verifyWallet(t, ctx, pool, b.WalletID, 10000, 3)
}
