package financial

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/junglegaming/backend-challenge-go/internal/domain/wager"
)

func TestCrossTransportKeyReplaysSavedObservedBalance(t *testing.T) {
	s, u := unitService()
	w := createUnitWallet(t, s, 10000)
	in := input(t, w.WalletID, wager.TypeBet, 2000)
	in.GameID = "game"
	in.IdempotencyKey = ScopedIdempotencyKey(string(in.ProviderID), "shared-key")
	first := runUnitWager(t, s, in)
	win := input(t, w.WalletID, wager.TypeWin, 5000)
	win.ExternalTransactionID, win.IdempotencyKey = "win", "win-key"
	runUnitWager(t, s, win)
	delivery := DeliveryInput{ConsumerName: "consumer", MessageID: "delivery", PayloadHash: strings.Repeat("a", 64), IdempotencyKey: "shared-key", Wager: in.WagerInput}
	result, err := s.ProcessDelivery(context.Background(), delivery)
	if err != nil || result.Outcome != "replay" || !result.Financial.IdempotentReplay || result.Financial.ObservedBalance != first.ObservedBalance || u.state.wallets[w.WalletID].Balance().Cents() != 13000 {
		t.Fatal("cross-transport replay changed original result", result, err)
	}
	delivery.MessageID = "changed-game"
	delivery.Wager.GameID = "other-game"
	_, err = s.ProcessDelivery(context.Background(), delivery)
	if !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatal("game metadata was omitted from conflict detection", err)
	}
}

func TestReversalsRequirePositiveAmounts(t *testing.T) {
	for _, kind := range []wager.Type{wager.TypeRefund, wager.TypeRollback} {
		s, _ := unitService()
		w := createUnitWallet(t, s, 10000)
		_, err := s.ProcessHTTPWager(context.Background(), reversalInput(t, w.WalletID, kind, 0, "zero", "missing"))
		if !errors.Is(err, ErrInvalidAmount) {
			t.Fatal("zero reversal accepted", kind, err)
		}
	}
}

func TestWinCanReferenceProcessedBetInSameRound(t *testing.T) {
	s, u := unitService()
	w := createUnitWallet(t, s, 10000)
	bet := input(t, w.WalletID, wager.TypeBet, 2000)
	bet.GameID = "game"
	runUnitWager(t, s, bet)
	win := input(t, w.WalletID, wager.TypeWin, 5000)
	win.GameID, win.ExternalTransactionID, win.IdempotencyKey = "game", "win", "win-key"
	win.ReferenceExternalTransactionID = bet.ExternalTransactionID
	result := runUnitWager(t, s, win)
	if result.State != wager.StateProcessed || u.state.wallets[w.WalletID].Balance().Cents() != 13000 {
		t.Fatal(result)
	}
	win.ExternalTransactionID, win.IdempotencyKey, win.RoundID = "wrong-round", "wrong-round-key", "other-round"
	result = runUnitWager(t, s, win)
	if result.State != wager.StateRejected || result.FailureCode != FailureReferenceRoundMismatch || u.state.wallets[w.WalletID].Balance().Cents() != 13000 {
		t.Fatal(result)
	}
}
