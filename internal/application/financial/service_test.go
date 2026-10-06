package financial

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"testing"
	"time"

	"github.com/junglegaming/backend-challenge-go/internal/domain/money"
	"github.com/junglegaming/backend-challenge-go/internal/domain/wager"
	"github.com/junglegaming/backend-challenge-go/internal/domain/wallet"
	"github.com/junglegaming/backend-challenge-go/internal/ports"
)

// Sequential unit-test double only. PostgreSQL concurrency is tested separately.
type memoryState struct {
	wallets   map[wallet.ID]wallet.Wallet
	wagers    map[wager.TransactionID]wager.Transaction
	ledger    map[string]ports.LedgerEntry
	outbox    map[string]ports.OutboxEvent
	keys      map[string]ports.IdempotencyRecord
	reversals map[wager.TransactionID]ports.Reversal
	pending   map[wager.TransactionID]ports.PendingReference
	inbox     map[[2]string]ports.InboxMessage
}

func newMemory() *memoryState {
	return &memoryState{wallets: map[wallet.ID]wallet.Wallet{}, wagers: map[wager.TransactionID]wager.Transaction{}, ledger: map[string]ports.LedgerEntry{}, outbox: map[string]ports.OutboxEvent{}, keys: map[string]ports.IdempotencyRecord{}, reversals: map[wager.TransactionID]ports.Reversal{}, pending: map[wager.TransactionID]ports.PendingReference{}, inbox: map[[2]string]ports.InboxMessage{}}
}
func copyMap[K comparable, V any](in map[K]V) map[K]V {
	out := make(map[K]V, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

type memoryUow struct {
	state          *memoryState
	failOutbox     bool
	failCompletion bool
}

func (u *memoryUow) WithinTransaction(ctx context.Context, fn func(ports.Repositories) error) error {
	work := &memoryState{wallets: copyMap(u.state.wallets), wagers: copyMap(u.state.wagers), ledger: copyMap(u.state.ledger), outbox: copyMap(u.state.outbox), keys: copyMap(u.state.keys), reversals: copyMap(u.state.reversals), pending: copyMap(u.state.pending), inbox: copyMap(u.state.inbox)}
	rs := ports.Repositories{Wallets: walletDouble{work}, Wagers: wagerDouble{work}, Ledger: ledgerDouble{work}, Outbox: outboxDouble{work, u.failOutbox}, Idempotency: keyDouble{work, u.failCompletion}, Reversals: reversalDouble{work}, PendingReferences: pendingDouble{work}, Inbox: inboxDouble{work}}
	if err := fn(rs); err != nil {
		return err
	}
	u.state = work
	return nil
}

type walletDouble struct{ state *memoryState }

func (r walletDouble) Create(_ context.Context, w wallet.Wallet) error {
	for _, old := range r.state.wallets {
		if old.ID() == w.ID() || old.PlayerID() == w.PlayerID() && old.Currency() == w.Currency() {
			return ports.ErrUniqueViolation
		}
	}
	r.state.wallets[w.ID()] = w
	return nil
}
func (r walletDouble) Get(_ context.Context, id wallet.ID) (wallet.Wallet, error) {
	w, ok := r.state.wallets[id]
	if !ok {
		return w, ports.ErrNotFound
	}
	return w, nil
}
func (r walletDouble) GetForUpdate(ctx context.Context, id wallet.ID) (wallet.Wallet, error) {
	return r.Get(ctx, id)
}
func (r walletDouble) Update(_ context.Context, w wallet.Wallet, v int64) error {
	old, ok := r.state.wallets[w.ID()]
	if !ok || old.Version() != v {
		return ports.ErrConflict
	}
	r.state.wallets[w.ID()] = w
	return nil
}

type wagerDouble struct{ state *memoryState }

func (r wagerDouble) Create(_ context.Context, tx wager.Transaction) error {
	for _, old := range r.state.wagers {
		if old.ID() == tx.ID() || tx.Type() != wager.TypeOpening && old.ProviderID() == tx.ProviderID() && old.ExternalTransactionID() == tx.ExternalTransactionID() {
			return ports.ErrUniqueViolation
		}
	}
	r.state.wagers[tx.ID()] = tx
	return nil
}
func (r wagerDouble) Get(_ context.Context, id wager.TransactionID) (wager.Transaction, error) {
	tx, ok := r.state.wagers[id]
	if !ok {
		return tx, ports.ErrNotFound
	}
	return tx, nil
}
func (r wagerDouble) GetByExternalID(_ context.Context, p wager.ProviderID, e wager.ExternalTransactionID) (wager.Transaction, error) {
	for _, tx := range r.state.wagers {
		if tx.ProviderID() == p && tx.ExternalTransactionID() == e {
			return tx, nil
		}
	}
	return wager.Transaction{}, ports.ErrNotFound
}
func (r wagerDouble) Update(_ context.Context, tx wager.Transaction, s wager.State) error {
	old, ok := r.state.wagers[tx.ID()]
	if !ok || old.State() != s {
		return ports.ErrConflict
	}
	r.state.wagers[tx.ID()] = tx
	return nil
}

type ledgerDouble struct{ state *memoryState }

func (r ledgerDouble) Append(_ context.Context, e ports.LedgerEntry) error {
	r.state.ledger[e.ID] = e
	return nil
}
func (r ledgerDouble) Get(_ context.Context, id string) (ports.LedgerEntry, error) {
	e, ok := r.state.ledger[id]
	if !ok {
		return e, ports.ErrNotFound
	}
	return e, nil
}

type outboxDouble struct {
	state *memoryState
	fail  bool
}

func (r outboxDouble) Create(_ context.Context, e ports.OutboxEvent) error {
	if r.fail {
		return ports.ErrPersistence
	}
	r.state.outbox[e.EventID] = e
	return nil
}
func (r outboxDouble) Get(_ context.Context, id string) (ports.OutboxEvent, error) {
	e, ok := r.state.outbox[id]
	if !ok {
		return e, ports.ErrNotFound
	}
	return e, nil
}

type keyDouble struct {
	state *memoryState
	fail  bool
}

func (r keyDouble) Claim(_ context.Context, key, hash string, at time.Time) (bool, error) {
	if _, ok := r.state.keys[key]; ok {
		return false, nil
	}
	r.state.keys[key] = ports.IdempotencyRecord{Key: key, PayloadHash: hash, CreatedAt: at}
	return true, nil
}
func (r keyDouble) Get(_ context.Context, key string) (ports.IdempotencyRecord, error) {
	record, ok := r.state.keys[key]
	if !ok {
		return record, ports.ErrNotFound
	}
	return record, nil
}
func (r keyDouble) Complete(_ context.Context, record ports.IdempotencyRecord) error {
	if r.fail {
		return ports.ErrPersistence
	}
	r.state.keys[record.Key] = record
	return nil
}

func unitService() (*Service, *memoryUow) {
	u := &memoryUow{state: newMemory()}
	s := NewService(u)
	counter := 0
	s.newID = func() (string, error) { counter++; return fmt.Sprintf("id-%d", counter), nil }
	s.now = func() time.Time { return time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC) }
	return s, u
}
func cents(t *testing.T, n int64, currency string) money.Money {
	t.Helper()
	m, err := money.New(n, currency)
	if err != nil {
		t.Fatal(err)
	}
	return m
}
func input(t *testing.T, id wallet.ID, kind wager.Type, n int64) HTTPWagerInput {
	return HTTPWagerInput{WagerInput: WagerInput{ProviderID: "provider", ExternalTransactionID: "external", PlayerID: "player", WalletID: id, Type: kind, Amount: cents(t, n, "BRL"), RoundID: "round", CorrelationID: "correlation"}, IdempotencyKey: "key"}
}
func createUnitWallet(t *testing.T, s *Service, n int64) CreateWalletResult {
	t.Helper()
	w, err := s.CreateWallet(context.Background(), CreateWalletInput{PlayerID: "player", Currency: "BRL", InitialBalance: cents(t, n, "BRL"), CorrelationID: "correlation"})
	if err != nil {
		t.Fatal(err)
	}
	return w
}
func checkEvents(t *testing.T, state *memoryState, result WagerResult, before string, count int, direction ports.LedgerDirection) {
	t.Helper()
	found := 0
	for _, event := range state.outbox {
		var envelope struct {
			EventEnvelope
			Data json.RawMessage `json:"data"`
		}
		if err := json.Unmarshal(event.Payload, &envelope); err != nil {
			t.Fatal(err)
		}
		if envelope.CausationID != string(result.TransactionID) {
			continue
		}
		found++
		if envelope.EventID != event.EventID || envelope.EventType != event.EventType || envelope.Version != 1 || envelope.CorrelationID != "correlation" || envelope.OccurredAt.Location() != time.UTC || envelope.AggregateID != event.AggregateID {
			t.Fatalf("bad envelope: %+v", envelope)
		}
		if envelope.EventType == EventWalletBalanceChanged {
			var data BalanceEventData
			if err := json.Unmarshal(envelope.Data, &data); err != nil {
				t.Fatal(err)
			}
			if data.WalletID != result.WalletID || data.TransactionID != result.TransactionID || data.Direction != direction || data.BalanceBefore.Amount != before || data.BalanceAfter != result.ObservedBalance || data.WalletVersion != result.WalletVersion || data.Money != result.Amount {
				t.Fatalf("bad balance event: %+v", data)
			}
		} else {
			var data TransactionEventData
			if err := json.Unmarshal(envelope.Data, &data); err != nil {
				t.Fatal(err)
			}
			expected := EventWagerProcessed
			if result.State == wager.StateRejected {
				expected = EventWagerRejected
			}
			if envelope.EventType != expected || data.State != result.State || data.FailureCode != result.FailureCode || data.Money != result.Amount {
				t.Fatalf("bad transaction event: %+v", data)
			}
		}
	}
	if found != count {
		t.Fatalf("events=%d want=%d", found, count)
	}
}

func TestCreateWalletOpeningAndZero(t *testing.T) {
	for _, n := range []int64{0, 2500} {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			s, u := unitService()
			result := createUnitWallet(t, s, n)
			w := u.state.wallets[result.WalletID]
			if w.Balance().Cents() != n || w.Version() != 1 {
				t.Fatal("incorrect initial wallet")
			}
			if n == 0 {
				if result.OpeningTransactionID != "" || len(u.state.wagers) != 0 || len(u.state.ledger) != 0 || len(u.state.outbox) != 0 {
					t.Fatal("zero balance created financial artifacts")
				}
				return
			}
			tx := u.state.wagers[result.OpeningTransactionID]
			if tx.Type() != wager.TypeOpening || tx.State() != wager.StateProcessed || tx.ProviderID() != "" || tx.ExternalTransactionID() != "" {
				t.Fatal("invalid opening")
			}
			if len(u.state.wagers) != 1 || len(u.state.ledger) != 1 {
				t.Fatal("invalid opening artifacts")
			}
			for _, entry := range u.state.ledger {
				if entry.Direction != ports.Credit || entry.BalanceBeforeCents != 0 || entry.BalanceAfterCents != n || entry.AmountCents != n || entry.WalletVersion != 1 {
					t.Fatal(entry)
				}
			}
			checkEvents(t, u.state, WagerResult{TransactionID: tx.ID(), WalletID: w.ID(), State: tx.State(), Amount: tx.Amount().External(), ObservedBalance: w.Balance().External(), WalletVersion: 1}, "0.00", 2, ports.Credit)
			if _, err := s.CreateWallet(context.Background(), CreateWalletInput{PlayerID: "player", Currency: "BRL", InitialBalance: cents(t, 0, "BRL")}); !errors.Is(err, ErrWalletExists) {
				t.Fatal(err)
			}
		})
	}
}
func TestWagerBusinessOutcomes(t *testing.T) {
	for _, tc := range []struct {
		name                   string
		kind                   wager.Type
		balance, amount, after int64
		state                  wager.State
		failure                wager.FailureCode
		ledger, events         int
		direction              ports.LedgerDirection
	}{
		{"BET", wager.TypeBet, 10000, 2000, 8000, wager.StateProcessed, "", 1, 2, ports.Debit},
		{"insufficient", wager.TypeBet, 10000, 12000, 10000, wager.StateRejected, FailureInsufficientFunds, 0, 1, ports.Debit},
		{"WIN", wager.TypeWin, 10000, 5000, 15000, wager.StateProcessed, "", 1, 2, ports.Credit},
		{"overflow", wager.TypeWin, math.MaxInt64, 1, math.MaxInt64, wager.StateRejected, FailureOverflow, 0, 1, ports.Credit},
		{"LOSS", wager.TypeLoss, 10000, 0, 10000, wager.StateProcessed, "", 0, 1, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, u := unitService()
			w := createUnitWallet(t, s, tc.balance)
			openingLedgers, openingEvents := len(u.state.ledger), len(u.state.outbox)
			result, err := s.ProcessHTTPWager(context.Background(), input(t, w.WalletID, tc.kind, tc.amount))
			if err != nil {
				t.Fatal(err)
			}
			persisted := u.state.wallets[w.WalletID]
			wantVersion := int64(1)
			if tc.ledger > 0 {
				wantVersion++
			}
			if result.State != tc.state || result.FailureCode != tc.failure || persisted.Balance().Cents() != tc.after || persisted.Version() != wantVersion || result.ObservedBalance != persisted.Balance().External() || result.WalletVersion != wantVersion || result.IdempotentReplay {
				t.Fatalf("bad outcome %+v wallet=%v", result, persisted)
			}
			if len(u.state.ledger)-openingLedgers != tc.ledger || len(u.state.outbox)-openingEvents != tc.events {
				t.Fatal("wrong artifact counts")
			}
			tx := u.state.wagers[result.TransactionID]
			if tx.State() != tc.state || tx.FailureCode() != tc.failure {
				t.Fatal("state not persisted")
			}
			if err := tx.MarkProcessed(s.now()); !errors.Is(err, wager.ErrTerminalState) {
				t.Fatal("terminal state mutable")
			}
			for _, entry := range u.state.ledger {
				if entry.TransactionID == result.TransactionID && (entry.BalanceBeforeCents != tc.balance || entry.BalanceAfterCents != tc.after || entry.AmountCents != tc.amount || entry.Direction != tc.direction || entry.WalletVersion != wantVersion) {
					t.Fatal(entry)
				}
			}
			if tc.failure == FailureInsufficientFunds && !errors.Is(result.BusinessError(), ErrInsufficientFunds) {
				t.Fatal("unstable insufficient funds error")
			}
			if tc.failure == FailureOverflow && !errors.Is(result.BusinessError(), ErrOverflow) {
				t.Fatal("unstable overflow error")
			}
			checkEvents(t, u.state, result, cents(t, tc.balance, "BRL").DecimalString(), tc.events, tc.direction)
			replay, err := s.ProcessHTTPWager(context.Background(), input(t, w.WalletID, tc.kind, tc.amount))
			if err != nil || !replay.IdempotentReplay {
				t.Fatalf("replay=%+v error=%v", replay, err)
			}
			replay.IdempotentReplay = false
			if replay != result {
				t.Fatal("replay changed original result")
			}
		})
	}
}
func TestInvalidWagerInputsHaveNoEffects(t *testing.T) {
	for _, tc := range []struct {
		name   string
		modify func(*HTTPWagerInput)
		want   error
	}{
		{"zero BET", func(in *HTTPWagerInput) { in.Amount = cents(t, 0, "BRL") }, ErrInvalidAmount},
		{"zero WIN", func(in *HTTPWagerInput) { in.Type = wager.TypeWin; in.Amount = cents(t, 0, "BRL") }, ErrInvalidAmount},
		{"negative", func(in *HTTPWagerInput) { in.Amount = cents(t, -1, "BRL") }, ErrInvalidAmount},
		{"LOSS nonzero", func(in *HTTPWagerInput) { in.Type = wager.TypeLoss }, ErrInvalidAmount},
		{"player", func(in *HTTPWagerInput) { in.PlayerID = "other" }, ErrPlayerMismatch},
		{"currency", func(in *HTTPWagerInput) { in.Amount = cents(t, 100, "USD") }, ErrCurrencyMismatch},
		{"missing wallet", func(in *HTTPWagerInput) { in.WalletID = "missing" }, ErrWalletNotFound},
		{"OPENING", func(in *HTTPWagerInput) { in.Type = wager.TypeOpening }, ErrInvalidOperationType},
		{"REFUND without reference", func(in *HTTPWagerInput) { in.Type = wager.TypeRefund }, ErrInvalidInput},
		{"ROLLBACK without reference", func(in *HTTPWagerInput) { in.Type = wager.TypeRollback }, ErrInvalidInput},
		{"no key", func(in *HTTPWagerInput) { in.IdempotencyKey = " " }, ErrIdempotencyKeyRequired},
		{"no round", func(in *HTTPWagerInput) { in.RoundID = "" }, ErrInvalidInput},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, u := unitService()
			w := createUnitWallet(t, s, 0)
			before := u.state
			in := input(t, w.WalletID, wager.TypeBet, 100)
			tc.modify(&in)
			_, err := s.ProcessHTTPWager(context.Background(), in)
			if !errors.Is(err, tc.want) {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(before, u.state) {
				t.Fatal("invalid request mutated persistence")
			}
		})
	}
}
func TestReplayOriginalObservedBalanceAndConflicts(t *testing.T) {
	s, u := unitService()
	w := createUnitWallet(t, s, 10000)
	in := input(t, w.WalletID, wager.TypeBet, 2000)
	original, err := s.ProcessHTTPWager(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	win := input(t, w.WalletID, wager.TypeWin, 5000)
	win.ExternalTransactionID = "win"
	win.IdempotencyKey = "win-key"
	if _, err = s.ProcessHTTPWager(context.Background(), win); err != nil {
		t.Fatal(err)
	}
	in.CorrelationID = "different correlation"
	replay, err := s.ProcessHTTPWager(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if !replay.IdempotentReplay || replay.ObservedBalance.Amount != "80.00" || u.state.wallets[w.WalletID].Balance().DecimalString() != "130.00" {
		t.Fatal("replay used current balance")
	}
	replay.IdempotentReplay = false
	if replay != original {
		t.Fatal("original result changed")
	}
	before := u.state
	changed := in
	changed.Amount = cents(t, 1, "BRL")
	_, err = s.ProcessHTTPWager(context.Background(), changed)
	if !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, u.state) {
		t.Fatal("conflict changed state")
	}
	changed.Amount = cents(t, 0, "BRL")
	_, err = s.ProcessHTTPWager(context.Background(), changed)
	if !errors.Is(err, ErrIdempotencyConflict) || !reflect.DeepEqual(before, u.state) {
		t.Fatal("invalid changed payload must still conflict without effects")
	}
	changed = in
	changed.IdempotencyKey = "other-key"
	_, err = s.ProcessHTTPWager(context.Background(), changed)
	if !errors.Is(err, ErrDuplicateExternalTransaction) {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, u.state) {
		t.Fatal("external duplicate changed state")
	}
}
func TestRollbackOnOutboxOrCompletionFailure(t *testing.T) {
	for _, completion := range []bool{false, true} {
		t.Run(fmt.Sprint(completion), func(t *testing.T) {
			s, u := unitService()
			w := createUnitWallet(t, s, 10000)
			before := u.state
			u.failCompletion = completion
			u.failOutbox = !completion
			if _, err := s.ProcessHTTPWager(context.Background(), input(t, w.WalletID, wager.TypeBet, 100)); !errors.Is(err, ErrPersistence) {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(before, u.state) {
				t.Fatal("partial transaction committed")
			}
		})
	}
	s, u := unitService()
	u.failOutbox = true
	if _, err := s.CreateWallet(context.Background(), CreateWalletInput{PlayerID: "player", Currency: "BRL", InitialBalance: cents(t, 100, "BRL")}); !errors.Is(err, ErrPersistence) {
		t.Fatal(err)
	}
	if len(u.state.wallets) != 0 || len(u.state.wagers) != 0 || len(u.state.ledger) != 0 || len(u.state.outbox) != 0 {
		t.Fatal("partial opening committed")
	}
}
func TestCreateWalletValidation(t *testing.T) {
	for _, tc := range []struct {
		player, currency string
		balance          money.Money
		want             error
	}{
		{"player", "BRL", cents(t, -1, "BRL"), ErrInvalidAmount},
		{"player", "brl", cents(t, 1, "BRL"), ErrCurrencyMismatch},
		{"player", "USD", cents(t, 1, "BRL"), ErrCurrencyMismatch},
		{"", "BRL", cents(t, 1, "BRL"), ErrInvalidInput},
	} {
		s, u := unitService()
		_, err := s.CreateWallet(context.Background(), CreateWalletInput{PlayerID: wallet.PlayerID(tc.player), Currency: tc.currency, InitialBalance: tc.balance})
		if !errors.Is(err, tc.want) || len(u.state.wallets) != 0 {
			t.Fatal(err)
		}
	}
}
func TestVersionOverflowIsPersistedRejection(t *testing.T) {
	s, u := unitService()
	w, err := wallet.Rehydrate("wallet", "player", "BRL", cents(t, 100, "BRL"), math.MaxInt64)
	if err != nil {
		t.Fatal(err)
	}
	u.state.wallets[w.ID()] = w
	result, err := s.ProcessHTTPWager(context.Background(), input(t, w.ID(), wager.TypeBet, 1))
	if err != nil || result.FailureCode != FailureOverflow || u.state.wallets[w.ID()] != w || len(u.state.ledger) != 0 {
		t.Fatalf("result=%+v error=%v", result, err)
	}
}
func TestTransportIndependentProcess(t *testing.T) {
	s, u := unitService()
	w := createUnitWallet(t, s, 10000)
	in := input(t, w.WalletID, wager.TypeBet, 100).WagerInput
	result, err := s.ProcessWager(context.Background(), in)
	if err != nil || result.State != wager.StateProcessed || len(u.state.keys) != 0 {
		t.Fatalf("result=%+v error=%v", result, err)
	}
	if _, err = s.ProcessWager(context.Background(), in); !errors.Is(err, ErrDuplicateExternalTransaction) {
		t.Fatal(err)
	}
}

func TestApplicationErrorBoundary(t *testing.T) {
	for _, tc := range []struct{ source, want error }{
		{fmt.Errorf("internal connection detail: %w", context.Canceled), context.Canceled},
		{fmt.Errorf("internal connection detail: %w", context.DeadlineExceeded), context.DeadlineExceeded},
		{ports.ErrConstraintViolation, ErrPersistence},
		{ports.ErrTransaction, ErrPersistence},
		{errors.New("unexpected internal detail"), ErrPersistence},
		{fmt.Errorf("internal context: %w", ErrIdempotencyConflict), ErrIdempotencyConflict},
	} {
		if got := applicationError(tc.source); got != tc.want {
			t.Fatalf("got %v want %v", got, tc.want)
		}
	}
}
