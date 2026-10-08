package wallet_test

import (
	"errors"
	"testing"
	"time"

	"github.com/junglegaming/backend-challenge-go/internal/domain/wallet"
)

func TestWalletTimestampsSurviveRehydrationAndRejectedDebit(t *testing.T) {
	w, err := wallet.New("wallet", "player", "BRL", mustMoney(t, 10000, "BRL"))
	if err != nil {
		t.Fatal(err)
	}
	if w.CreatedAt().IsZero() || !w.UpdatedAt().Equal(w.CreatedAt()) {
		t.Fatal("creation timestamps missing")
	}
	restored, err := wallet.RehydrateWithTimestamps(w.ID(), w.PlayerID(), w.Currency(), w.Balance(), w.Version(), w.CreatedAt(), w.UpdatedAt())
	if err != nil || restored != w {
		t.Fatal("rehydration changed persisted state", err)
	}
	if err := restored.Debit(mustMoney(t, 20000, "BRL")); !errors.Is(err, wallet.ErrInsufficientFunds) {
		t.Fatal(err)
	}
	if restored != w {
		t.Fatal("rejected debit changed metadata")
	}
	if err := restored.Debit(mustMoney(t, 100, "BRL")); err != nil {
		t.Fatal(err)
	}
	if !restored.CreatedAt().Equal(w.CreatedAt()) || restored.UpdatedAt().Before(w.UpdatedAt()) || restored.Version() != 2 {
		t.Fatal("movement corrupted timestamps")
	}
	if _, err := wallet.RehydrateWithTimestamps(w.ID(), w.PlayerID(), w.Currency(), w.Balance(), 1, time.Time{}, w.UpdatedAt()); !errors.Is(err, wallet.ErrInvalidTimestamp) {
		t.Fatal("invalid timestamps accepted", err)
	}
}
