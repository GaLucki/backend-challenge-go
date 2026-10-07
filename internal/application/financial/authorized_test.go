package financial

import (
	"context"
	"errors"
	"testing"

	"github.com/junglegaming/backend-challenge-go/internal/domain/money"
	"github.com/junglegaming/backend-challenge-go/internal/domain/wager"
	"github.com/junglegaming/backend-challenge-go/internal/identity"
)

func TestAuthorizedWritesRejectSpoofingBeforePersistence(t *testing.T) {
	s := NewAuthorizedService(nil, identity.NewAuthorizer(), nil, nil)
	p := identity.Principal{Subject: "a", ClientID: "a", ProviderID: "a", Roles: []string{identity.RoleProvider}}
	if _, err := s.ProcessHTTPWager(context.Background(), p, HTTPWagerInput{WagerInput: WagerInput{ProviderID: "b"}}); !errors.Is(err, identity.ErrForbidden) {
		t.Fatal("spoofing accepted", err)
	}
	if _, err := s.CreateWallet(context.Background(), p, CreateWalletInput{}); !errors.Is(err, identity.ErrForbidden) {
		t.Fatal("provider opened wallet", err)
	}
	if _, err := s.GetWallet(context.Background(), p, "wallet"); !errors.Is(err, identity.ErrForbidden) {
		t.Fatal("provider read unscoped wallet", err)
	}
	if _, err := s.GetByExternalID(context.Background(), p, "b", "external"); !errors.Is(err, identity.ErrForbidden) {
		t.Fatal("provider read foreign external ID", err)
	}
	if _, err := s.GetTransaction(context.Background(), identity.Principal{}, "tx"); !errors.Is(err, identity.ErrUnauthorized) {
		t.Fatal("anonymous lookup accepted", err)
	}
}

func TestAuthorizedIdempotencyIsScopedByProvider(t *testing.T) {
	ctx := context.Background()
	u := &memoryUow{state: newMemory()}
	core := NewService(u)
	s := NewAuthorizedService(core, identity.NewAuthorizer(), nil, nil)
	internal := identity.Principal{Subject: "internal", ClientID: "internal", Internal: true, Roles: []string{identity.RoleInternal}}
	initial, _ := money.New(1000, "BRL")
	w, err := s.CreateWallet(ctx, internal, CreateWalletInput{PlayerID: "player", Currency: "BRL", InitialBalance: initial})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ProcessHTTPWager(ctx, internal, HTTPWagerInput{}); !errors.Is(err, identity.ErrForbidden) {
		t.Fatal("internal client impersonated provider")
	}
	var ids []wager.TransactionID
	for _, id := range []string{"a", "b"} {
		p := identity.Principal{Subject: id, ClientID: id, ProviderID: id, Roles: []string{identity.RoleProvider}}
		in := input(t, w.WalletID, wager.TypeBet, 100)
		in.ProviderID = ""
		in.IdempotencyKey = "same-client-key"
		first, err := s.ProcessHTTPWager(ctx, p, in)
		if err != nil {
			t.Fatal(err)
		}
		if string(first.ProviderID) != id || first.IdempotentReplay {
			t.Fatal("identity not derived from principal")
		}
		replay, err := s.ProcessHTTPWager(ctx, p, in)
		if err != nil || replay.TransactionID != first.TransactionID || !replay.IdempotentReplay {
			t.Fatal("own replay failed", err)
		}
		ids = append(ids, first.TransactionID)
	}
	if ids[0] == ids[1] || len(u.state.keys) != 2 {
		t.Fatal("providers shared replay records")
	}
	if u.state.wallets[w.WalletID].Balance().Cents() != 800 {
		t.Fatal("replay duplicated financial effect")
	}
}
