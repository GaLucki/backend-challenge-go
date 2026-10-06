package wager

import (
	"errors"
	"github.com/junglegaming/backend-challenge-go/internal/domain/money"
	"testing"
	"time"
)

func TestRehydratePreservesPersistedState(t *testing.T) {
	amount, _ := money.New(100, "BRL")
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	p := PersistedState{ExternalParams: ExternalParams{ID: "tx", ProviderID: "p", ExternalTransactionID: "e", PlayerID: "player", WalletID: "wallet", RoundID: "round", Type: TypeBet, Amount: amount}, State: StateFailed, FailureCode: "FAILED", CreatedAt: at, UpdatedAt: at.Add(time.Minute)}
	tx, err := Rehydrate(p)
	if err != nil || tx.State() != StateFailed || tx.FailureCode() != "FAILED" || !tx.CreatedAt().Equal(at) || !tx.UpdatedAt().Equal(p.UpdatedAt) {
		t.Fatalf("tx=%v err=%v", tx, err)
	}
	if err := tx.MarkProcessed(at); !errors.Is(err, ErrTerminalState) {
		t.Fatal(err)
	}
	p.ProviderID = ""
	if _, err := Rehydrate(p); !errors.Is(err, ErrInvalidProviderID) {
		t.Fatal(err)
	}
}
func TestRehydrateOpening(t *testing.T) {
	amount, _ := money.New(0, "BRL")
	p := PersistedState{ExternalParams: ExternalParams{ID: "tx", PlayerID: "player", WalletID: "wallet", Type: TypeOpening, Amount: amount}, State: StateProcessed}
	if _, err := Rehydrate(p); err != nil {
		t.Fatal(err)
	}
	p.State = StatePending
	if _, err := Rehydrate(p); !errors.Is(err, ErrInvalidState) {
		t.Fatal(err)
	}
}
