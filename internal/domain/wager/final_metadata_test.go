package wager_test

import (
	"errors"
	"testing"
	"time"

	"github.com/junglegaming/backend-challenge-go/internal/domain/money"
	"github.com/junglegaming/backend-challenge-go/internal/domain/wager"
)

func TestGameMetadataValidationAndRehydration(t *testing.T) {
	m, _ := money.New(100, "BRL")
	p := wager.ExternalParams{ID: "id", ProviderID: "provider", ExternalTransactionID: "external", PlayerID: "player", WalletID: "wallet", RoundID: "round", GameID: "game", Type: wager.TypeBet, Amount: m, Now: time.Now().UTC()}
	tx, err := wager.NewExternal(p)
	if err != nil || tx.GameID() != "game" {
		t.Fatal("game not preserved", err)
	}
	p.ID = ""
	if invalid, err := wager.NewExternal(p); err == nil || invalid != (wager.Transaction{}) {
		t.Fatal("constructor returned partially initialized game metadata on error", err)
	}
	p.ID = "id"
	p.GameID = " game "
	if _, err := wager.NewExternal(p); !errors.Is(err, wager.ErrInvalidGameID) {
		t.Fatal("invalid game metadata accepted", err)
	}
	p.GameID, p.Type, p.ProviderID, p.ExternalTransactionID, p.RoundID = "game", wager.TypeOpening, "", "", ""
	if _, err := wager.Rehydrate(wager.PersistedState{ExternalParams: p, State: wager.StateProcessed, CreatedAt: p.Now, UpdatedAt: p.Now}); !errors.Is(err, wager.ErrInvalidState) {
		t.Fatal("opening accepted external game metadata", err)
	}
}
