package financial

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/junglegaming/backend-challenge-go/internal/domain/money"
	"github.com/junglegaming/backend-challenge-go/internal/domain/wager"
	"github.com/junglegaming/backend-challenge-go/internal/domain/wallet"
	"github.com/junglegaming/backend-challenge-go/internal/identity"
	"github.com/junglegaming/backend-challenge-go/internal/ports"
)

type readerSpy struct {
	snapshot     ports.TransactionSnapshot
	entries      []ports.LedgerEntry
	after        ports.LedgerPosition
	limit, calls int
}

func (s *readerSpy) ListLedger(_ context.Context, _ wallet.ID, after ports.LedgerPosition, limit int) ([]ports.LedgerEntry, error) {
	s.calls++
	s.after = after
	s.limit = limit
	return s.entries, nil
}
func (s *readerSpy) GetTransactionSnapshot(_ context.Context, _ wager.TransactionID) (ports.TransactionSnapshot, error) {
	s.calls++
	return s.snapshot, nil
}

func readFixture(t *testing.T) (*ReadService, *readerSpy, identity.Principal) {
	t.Helper()
	m, _ := money.New(1000, "BRL")
	w, _ := wallet.New("wallet", "player", "BRL", m)
	state := newMemory()
	state.wallets[w.ID()] = w
	spy := &readerSpy{}
	secured := NewAuthorizedService(nil, identity.NewAuthorizer(), wagerDouble{state}, walletDouble{state})
	return NewReadService(secured, spy), spy, identity.Principal{Subject: "internal", ClientID: "internal", Internal: true, Roles: []string{identity.RoleInternal}}
}

func TestLedgerCursorBindsWalletAndStablePosition(t *testing.T) {
	s, spy, p := readFixture(t)
	spy.entries = []ports.LedgerEntry{{ID: "a", WalletVersion: 1}, {ID: "b", WalletVersion: 2}, {ID: "c", WalletVersion: 3}}
	first, err := s.Ledger(context.Background(), p, "wallet", "", 2)
	if err != nil || len(first.Entries) != 2 || first.NextCursor == "" || spy.limit != 3 {
		t.Fatal("first page invalid", err)
	}
	spy.entries = []ports.LedgerEntry{{ID: "c", WalletVersion: 3}}
	last, err := s.Ledger(context.Background(), p, "wallet", first.NextCursor, 2)
	if err != nil || len(last.Entries) != 1 || last.NextCursor != "" || spy.after.WalletVersion != 2 || spy.after.EntryID != "b" {
		t.Fatal("cursor continuation invalid", err)
	}
	spy.entries = []ports.LedgerEntry{}
	empty, err := s.Ledger(context.Background(), p, "wallet", "", 50)
	if err != nil || len(empty.Entries) != 0 || empty.NextCursor != "" {
		t.Fatal("empty ledger invalid")
	}
	for _, cursor := range []string{"bad", base64.RawURLEncoding.EncodeToString([]byte(`{"v":1,"w":"other-wallet","n":2,"i":"b"}`)), base64.RawURLEncoding.EncodeToString([]byte(`{"v":2,"w":"wallet","n":2,"i":"b"}`)), base64.RawURLEncoding.EncodeToString([]byte(`{"v":1,"w":"wallet","n":0,"i":"b"}`)), base64.RawURLEncoding.EncodeToString([]byte(`{"v":1,"w":"wallet","n":2,"i":"b","extra":true}`)), first.NextCursor + "="} {
		if _, err := s.Ledger(context.Background(), p, "wallet", cursor, 50); !errors.Is(err, ErrInvalidPagination) {
			t.Fatal("invalid cursor accepted", cursor, err)
		}
	}
	for _, limit := range []int{0, -1, 101} {
		if _, err := s.Ledger(context.Background(), p, "wallet", "", limit); !errors.Is(err, ErrInvalidPagination) {
			t.Fatal("bad limit accepted")
		}
	}
	provider := identity.Principal{Subject: "p", ClientID: "p", ProviderID: "a", Roles: []string{identity.RoleProvider}}
	before := spy.calls
	if _, err := s.Ledger(context.Background(), provider, "wallet", first.NextCursor, 50); !errors.Is(err, identity.ErrForbidden) || spy.calls != before {
		t.Fatal("provider read unscoped ledger")
	}
}

func TestTransactionReadsAuthorizeBeforeReturningStoredResult(t *testing.T) {
	s, spy, _ := readFixture(t)
	amount, _ := money.New(100, "BRL")
	tx, _ := wager.NewExternal(wager.ExternalParams{ID: "tx", ProviderID: "a", ExternalTransactionID: "ext", PlayerID: "player", WalletID: "wallet", RoundID: "round", Type: wager.TypeBet, Amount: amount, Now: time.Now().UTC()})
	_ = tx.MarkProcessed(time.Now().UTC())
	result := WagerResult{TransactionID: "tx", ProviderID: "a", State: wager.StateProcessed, ObservedBalance: money.External{Amount: "9.00", Currency: "BRL"}}
	raw, _ := json.Marshal(result)
	spy.snapshot = ports.TransactionSnapshot{Transaction: tx, SavedResult: raw}
	p := identity.Principal{Subject: "a", ClientID: "a", ProviderID: "a", Roles: []string{identity.RoleProvider}}
	detail, err := s.Transaction(context.Background(), p, "tx")
	if err != nil || detail.Result == nil || detail.Result.ObservedBalance.Amount != "9.00" {
		t.Fatal("stored observed result not returned", err)
	}
	foreign := p
	foreign.ProviderID = "b"
	spy.snapshot.SavedResult = []byte("invalid-json")
	if _, err := s.Transaction(context.Background(), foreign, "tx"); !errors.Is(err, identity.ErrForbidden) {
		t.Fatal("foreign data processed before authorization", err)
	}
	if _, err := s.Transaction(context.Background(), p, "tx"); !errors.Is(err, ErrPersistence) {
		t.Fatal("corrupt stored result accepted")
	}
	spy.snapshot.SavedResult = nil
	detail, err = s.Transaction(context.Background(), p, "tx")
	if err != nil || detail.Result != nil {
		t.Fatal("non-HTTP result was invented")
	}
}
