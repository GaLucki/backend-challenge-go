package financial

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/junglegaming/backend-challenge-go/internal/domain/money"

	"github.com/junglegaming/backend-challenge-go/internal/domain/wager"
	"github.com/junglegaming/backend-challenge-go/internal/domain/wallet"
	"github.com/junglegaming/backend-challenge-go/internal/ports"
)

func reversalInput(t *testing.T, id wallet.ID, kind wager.Type, amount int64, external, reference string) HTTPWagerInput {
	in := input(t, id, kind, amount)
	in.ExternalTransactionID = wager.ExternalTransactionID(external)
	in.IdempotencyKey = external + "-key"
	in.ReferenceExternalTransactionID = wager.ExternalTransactionID(reference)
	return in
}
func runUnitWager(t *testing.T, s *Service, in HTTPWagerInput) WagerResult {
	t.Helper()
	result, err := s.ProcessHTTPWager(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	return result
}
func TestReversalFinancialPaths(t *testing.T) {
	for _, tc := range []struct {
		name              string
		original, reverse wager.Type
		amount            int64
		before, after     string
		direction         ports.LedgerDirection
	}{
		{"REFUND of BET", wager.TypeBet, wager.TypeRefund, 2000, "80.00", "100.00", ports.Credit},
		{"ROLLBACK of BET", wager.TypeBet, wager.TypeRollback, 2000, "80.00", "100.00", ports.Credit},
		{"ROLLBACK of WIN", wager.TypeWin, wager.TypeRollback, 5000, "150.00", "100.00", ports.Debit},
		{"ROLLBACK of REFUND", wager.TypeRefund, wager.TypeRollback, 2000, "100.00", "80.00", ports.Debit},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, u := unitService()
			w := createUnitWallet(t, s, 10000)
			in := input(t, w.WalletID, tc.original, tc.amount)
			if tc.original == wager.TypeRefund {
				bet := input(t, w.WalletID, wager.TypeBet, tc.amount)
				bet.ExternalTransactionID = "bet"
				bet.IdempotencyKey = "bet-key"
				runUnitWager(t, s, bet)
				in.ReferenceExternalTransactionID = "bet"
			}
			original := runUnitWager(t, s, in)
			result := runUnitWager(t, s, reversalInput(t, w.WalletID, tc.reverse, tc.amount, "reverse", "external"))
			if result.State != wager.StateProcessed || result.ObservedBalance.Amount != tc.after || result.WalletVersion != original.WalletVersion+1 {
				t.Fatal(result)
			}
			record := u.state.reversals[original.TransactionID]
			if record.ReversalTransactionID != result.TransactionID {
				t.Fatal("reversal claim missing")
			}
			entry := u.state.ledger[string(result.TransactionID)+":ledger"]
			if entry.Direction != tc.direction || entry.BalanceBeforeCents != mustParseCents(t, tc.before) || entry.BalanceAfterCents != mustParseCents(t, tc.after) {
				t.Fatal(entry)
			}
			checkEvents(t, u.state, result, tc.before, 2, tc.direction)
			replay := runUnitWager(t, s, reversalInput(t, w.WalletID, tc.reverse, tc.amount, "reverse", "external"))
			if !replay.IdempotentReplay {
				t.Fatal("reversal replay reapplied")
			}
		})
	}
}
func mustParseCents(t *testing.T, text string) int64 {
	t.Helper()
	m, err := money.ParseDecimal(text, "BRL")
	if err != nil {
		t.Fatal(err)
	}
	return m.Cents()
}

func TestReferenceValidationFailures(t *testing.T) {
	base := wager.ExternalParams{ID: "ref", ProviderID: "provider", ExternalTransactionID: "external", PlayerID: "player", WalletID: "wallet", RoundID: "round", Type: wager.TypeBet, Amount: cents(t, 2000, "BRL")}
	txParams := base
	txParams.ID = "reverse"
	txParams.Type = wager.TypeRefund
	txParams.ExternalTransactionID = "reverse"
	txParams.ReferenceExternalTransactionID = "external"
	tx, err := wager.NewExternal(txParams)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name      string
		modify    func(*wager.ExternalParams)
		processed bool
		code      wager.FailureCode
	}{
		{"provider", func(p *wager.ExternalParams) { p.ProviderID = "other" }, true, FailureReferenceProviderMismatch},
		{"player", func(p *wager.ExternalParams) { p.PlayerID = "other" }, true, FailureReferencePlayerMismatch},
		{"wallet", func(p *wager.ExternalParams) { p.WalletID = "other" }, true, FailureReferenceWalletMismatch},
		{"currency", func(p *wager.ExternalParams) { p.Amount = cents(t, 2000, "USD") }, true, FailureReferenceCurrencyMismatch},
		{"round", func(p *wager.ExternalParams) { p.RoundID = "other" }, true, FailureReferenceRoundMismatch},
		{"amount", func(p *wager.ExternalParams) { p.Amount = cents(t, 1900, "BRL") }, true, FailureReferenceAmountMismatch},
		{"type", func(p *wager.ExternalParams) { p.Type = wager.TypeWin }, true, FailureReferenceTypeInvalid},
		{"not processed", func(p *wager.ExternalParams) {}, false, FailureReferenceStateInvalid},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := base
			tc.modify(&p)
			ref, err := wager.NewExternal(p)
			if err != nil {
				t.Fatal(err)
			}
			if tc.processed {
				if err := ref.MarkProcessed(time.Now()); err != nil {
					t.Fatal(err)
				}
			}
			_, code := validateReference(tx, ref)
			if code != tc.code {
				t.Fatalf("got %s want %s", code, tc.code)
			}
		})
	}
	txParams.Type = wager.TypeRollback
	tx, err = wager.NewExternal(txParams)
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []wager.Type{wager.TypeLoss, wager.TypeOpening, wager.TypeRollback} {
		var ref wager.Transaction
		if kind == wager.TypeOpening {
			ref, err = wager.NewOpening(wager.OpeningParams{ID: "ref", PlayerID: "player", WalletID: "wallet", Amount: base.Amount})
		} else {
			p := base
			p.Type = kind
			ref, err = wager.NewExternal(p)
			if err == nil {
				err = ref.MarkProcessed(time.Now())
			}
		}
		if err != nil {
			t.Fatal(err)
		}
		_, code := validateReference(tx, ref)
		if code != FailureReferenceTypeInvalid {
			t.Fatalf("%s code=%s", kind, code)
		}
	}
	for _, state := range []wager.State{wager.StateRejected, wager.StateFailed, wager.StatePending, wager.StatePendingReference} {
		ref, err := wager.Rehydrate(wager.PersistedState{ExternalParams: base, State: state})
		if err != nil {
			t.Fatal(err)
		}
		_, code := validateReference(tx, ref)
		if code != FailureReferenceStateInvalid {
			t.Fatalf("%s code=%s", state, code)
		}
	}
}

func TestRollbackLOSSHasPersistedRejection(t *testing.T) {
	s, u := unitService()
	w := createUnitWallet(t, s, 10000)
	runUnitWager(t, s, input(t, w.WalletID, wager.TypeLoss, 0))
	result := runUnitWager(t, s, reversalInput(t, w.WalletID, wager.TypeRollback, 1, "rollback", "external"))
	if result.State != wager.StateRejected || result.FailureCode != FailureReferenceTypeInvalid || u.state.wallets[w.WalletID].Version() != 1 {
		t.Fatal(result)
	}
}
func TestDuplicateReversalsAndCrossMeaning(t *testing.T) {
	for _, first := range []wager.Type{wager.TypeRefund, wager.TypeRollback} {
		for _, second := range []wager.Type{wager.TypeRefund, wager.TypeRollback} {
			t.Run(string(first)+"/"+string(second), func(t *testing.T) {
				s, u := unitService()
				w := createUnitWallet(t, s, 10000)
				runUnitWager(t, s, input(t, w.WalletID, wager.TypeBet, 2000))
				firstResult := runUnitWager(t, s, reversalInput(t, w.WalletID, first, 2000, "first", "external"))
				before := u.state.wallets[w.WalletID]
				duplicate := runUnitWager(t, s, reversalInput(t, w.WalletID, second, 2000, "second", "external"))
				if duplicate.State != wager.StateRejected || duplicate.FailureCode != FailureAlreadyReversed || u.state.wallets[w.WalletID] != before {
					t.Fatal(duplicate)
				}
				if _, ok := u.state.ledger[string(duplicate.TransactionID)+":ledger"]; ok {
					t.Fatal("duplicate ledger")
				}
				if first == wager.TypeRefund {
					runUnitWager(t, s, reversalInput(t, w.WalletID, wager.TypeRollback, 2000, "undo-refund", "first"))
					again := runUnitWager(t, s, reversalInput(t, w.WalletID, wager.TypeRefund, 2000, "third", "external"))
					if again.FailureCode != FailureAlreadyReversed {
						t.Fatal("rollback of refund reopened BET")
					}
				}
				if firstResult.State != wager.StateProcessed {
					t.Fatal(firstResult)
				}
			})
		}
	}
}
func TestReversalInsufficientFundsAndRollbackFailure(t *testing.T) {
	s, u := unitService()
	w := createUnitWallet(t, s, 10000)
	win := input(t, w.WalletID, wager.TypeWin, 5000)
	runUnitWager(t, s, win)
	bet := input(t, w.WalletID, wager.TypeBet, 13000)
	bet.ExternalTransactionID = "bet"
	bet.IdempotencyKey = "bet-key"
	runUnitWager(t, s, bet)
	before := u.state.wallets[w.WalletID]
	in := reversalInput(t, w.WalletID, wager.TypeRollback, 5000, "rollback", "external")
	result := runUnitWager(t, s, in)
	if result.FailureCode != FailureInsufficientFundsForReversal || !errors.Is(result.BusinessError(), ErrInsufficientFundsForReversal) || u.state.wallets[w.WalletID] != before || len(u.state.reversals) != 0 {
		t.Fatal(result)
	}
	checkEvents(t, u.state, result, "20.00", 1, "")
	// A rejected reversal consumes no successful-reversal claim.
	credit := input(t, w.WalletID, wager.TypeWin, 5000)
	credit.ExternalTransactionID = "credit"
	credit.IdempotencyKey = "credit-key"
	runUnitWager(t, s, credit)
	valid := reversalInput(t, w.WalletID, wager.TypeRollback, 5000, "valid", "external")
	snapshot := u.state
	u.failOutbox = true
	_, err := s.ProcessHTTPWager(context.Background(), valid)
	if !errors.Is(err, ErrPersistence) || !reflect.DeepEqual(snapshot, u.state) {
		t.Fatal("partial reversal survived event failure")
	}
	u.failOutbox = false
	result = runUnitWager(t, s, valid)
	if result.State != wager.StateProcessed {
		t.Fatal(result)
	}
}
func TestPendingMetadataEventsRecoveryAndResult(t *testing.T) {
	s, u := unitService()
	w := createUnitWallet(t, s, 10000)
	in := reversalInput(t, w.WalletID, wager.TypeRefund, 2000, "refund", "late-bet")
	pending := runUnitWager(t, s, in)
	if pending.State != wager.StatePendingReference || pending.WalletVersion != 1 {
		t.Fatal(pending)
	}
	p := u.state.pending[pending.TransactionID]
	if p.AttemptCount != 0 || p.MaxAttempts != 10 || !p.NextAttemptAt.Equal(s.now().Add(time.Second)) || !p.ExpiresAt.Equal(s.now().Add(15*time.Minute)) {
		t.Fatal(p)
	}
	var event EventEnvelope
	raw := u.state.outbox[string(pending.TransactionID)+":"+EventWagerPendingReference]
	if err := json.Unmarshal(raw.Payload, &event); err != nil {
		t.Fatal(err)
	}
	if event.EventType != EventWagerPendingReference {
		t.Fatal(event)
	}
	ledgerCount, eventCount := len(u.state.ledger), len(u.state.outbox)
	replay := runUnitWager(t, s, in)
	if !replay.IdempotentReplay || replay.State != wager.StatePendingReference {
		t.Fatal(replay)
	}
	handled, err := s.ResolvePendingOnce(context.Background(), p.NextAttemptAt)
	if err != nil || !handled {
		t.Fatal(err)
	}
	p = u.state.pending[p.TransactionID]
	if p.AttemptCount != 1 || p.LastAttemptAt == nil || len(u.state.outbox) != eventCount || len(u.state.ledger) != ledgerCount {
		t.Fatal("retry generated artifacts")
	}
	bet := input(t, w.WalletID, wager.TypeBet, 2000)
	bet.ExternalTransactionID = "late-bet"
	bet.IdempotencyKey = "late-key"
	runUnitWager(t, s, bet)
	// New service, same durable repository state, no scheduler state carried over.
	recovered := NewService(u)
	handled, err = recovered.ResolvePendingOnce(context.Background(), p.NextAttemptAt)
	if err != nil || !handled {
		t.Fatal(err)
	}
	replay = runUnitWager(t, recovered, in)
	if replay.State != wager.StateProcessed || replay.ObservedBalance.Amount != "100.00" || !replay.IdempotentReplay {
		t.Fatal(replay)
	}
	if u.state.pending[p.TransactionID].CompletedAt == nil || u.state.wallets[w.WalletID].Version() != 3 {
		t.Fatal("recovery incomplete")
	}
	// Once terminal, balance in the saved result remains the terminal observed value.
	win := input(t, w.WalletID, wager.TypeWin, 5000)
	win.ExternalTransactionID = "win"
	win.IdempotencyKey = "win-key"
	runUnitWager(t, recovered, win)
	if replay = runUnitWager(t, recovered, in); replay.ObservedBalance.Amount != "100.00" {
		t.Fatal(replay)
	}
}
func TestPendingExpirationAndInvalidFoundReference(t *testing.T) {
	for _, mode := range []string{"max attempts", "TTL", "invalid reference"} {
		t.Run(mode, func(t *testing.T) {
			s, u := unitService()
			s.pendingPolicy.MaxAttempts = 1
			w := createUnitWallet(t, s, 10000)
			in := reversalInput(t, w.WalletID, wager.TypeRefund, 2000, "refund", "late")
			result := runUnitWager(t, s, in)
			p := u.state.pending[result.TransactionID]
			at := p.NextAttemptAt
			want := FailureReferenceNotFound
			if mode == "TTL" {
				at = p.ExpiresAt
			} else if mode == "invalid reference" {
				win := input(t, w.WalletID, wager.TypeWin, 2000)
				win.ExternalTransactionID = "late"
				win.IdempotencyKey = "late-key"
				runUnitWager(t, s, win)
				want = FailureReferenceTypeInvalid
			}
			handled, err := s.ResolvePendingOnce(context.Background(), at)
			if err != nil || !handled {
				t.Fatal(err)
			}
			replay := runUnitWager(t, s, in)
			if replay.State != wager.StateRejected || replay.FailureCode != want || !replay.IdempotentReplay {
				t.Fatal(replay)
			}
			if u.state.pending[result.TransactionID].CompletedAt == nil {
				t.Fatal("pending not finished")
			}
			count := len(u.state.outbox)
			handled, err = s.ResolvePendingOnce(context.Background(), at.Add(time.Hour))
			if err != nil || handled || len(u.state.outbox) != count {
				t.Fatal("terminal item retried")
			}
		})
	}
}
