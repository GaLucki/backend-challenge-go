package wager_test

import (
	"errors"
	"testing"
	"time"

	"github.com/junglegaming/backend-challenge-go/internal/domain/money"
	"github.com/junglegaming/backend-challenge-go/internal/domain/wager"
)

func mustAmount(t *testing.T, cents int64) money.Money {
	t.Helper()
	m, err := money.New(cents, "BRL")
	if err != nil {
		t.Fatalf("money.New() error = %v", err)
	}
	return m
}

func validExternal(t *testing.T, txType wager.Type) wager.ExternalParams {
	t.Helper()
	return wager.ExternalParams{
		ID:                    "tx-1",
		ProviderID:            "provider-a",
		ExternalTransactionID: "ext-1",
		PlayerID:              "player-1",
		WalletID:              "wallet-1",
		RoundID:               "round-1",
		Type:                  txType,
		Amount:                mustAmount(t, 2500),
		Now:                   time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC),
	}
}

func TestNewExternalTypes(t *testing.T) {
	for _, txType := range []wager.Type{
		wager.TypeBet, wager.TypeWin, wager.TypeLoss, wager.TypeRefund, wager.TypeRollback,
	} {
		tx, err := wager.NewExternal(validExternal(t, txType))
		if err != nil {
			t.Fatalf("NewExternal(%s) error = %v", txType, err)
		}
		if tx.State() != wager.StatePending {
			t.Fatalf("state = %s, want PENDING", tx.State())
		}
		if tx.Type() != txType {
			t.Fatalf("type = %s, want %s", tx.Type(), txType)
		}
	}
}

func TestRejectOpeningAsExternal(t *testing.T) {
	p := validExternal(t, wager.TypeOpening)
	if _, err := wager.NewExternal(p); !errors.Is(err, wager.ErrOpeningNotExternal) {
		t.Fatalf("error = %v, want ErrOpeningNotExternal", err)
	}
}

func TestNewOpeningInternal(t *testing.T) {
	tx, err := wager.NewOpening(wager.OpeningParams{
		ID:       "tx-open",
		PlayerID: "player-1",
		WalletID: "wallet-1",
		Amount:   mustAmount(t, 10000),
		Now:      time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("NewOpening() error = %v", err)
	}
	if tx.Type() != wager.TypeOpening {
		t.Fatalf("type = %s", tx.Type())
	}
	if tx.State() != wager.StateProcessed {
		t.Fatalf("state = %s, want PROCESSED", tx.State())
	}
	if tx.ProviderID() != "" || tx.ExternalTransactionID() != "" {
		t.Fatal("opening should not require external identifiers")
	}
}

func TestRequiredFields(t *testing.T) {
	base := validExternal(t, wager.TypeBet)

	p := base
	p.ID = ""
	if _, err := wager.NewExternal(p); !errors.Is(err, wager.ErrInvalidTransactionID) {
		t.Fatalf("id error = %v", err)
	}

	p = base
	p.ProviderID = ""
	if _, err := wager.NewExternal(p); !errors.Is(err, wager.ErrInvalidProviderID) {
		t.Fatalf("provider error = %v", err)
	}

	p = base
	p.ExternalTransactionID = ""
	if _, err := wager.NewExternal(p); !errors.Is(err, wager.ErrInvalidExternalID) {
		t.Fatalf("external id error = %v", err)
	}

	p = base
	p.PlayerID = ""
	if _, err := wager.NewExternal(p); !errors.Is(err, wager.ErrInvalidPlayerID) {
		t.Fatalf("player error = %v", err)
	}

	p = base
	p.WalletID = ""
	if _, err := wager.NewExternal(p); !errors.Is(err, wager.ErrInvalidWalletID) {
		t.Fatalf("wallet error = %v", err)
	}

	p = base
	p.RoundID = ""
	if _, err := wager.NewExternal(p); !errors.Is(err, wager.ErrInvalidRoundID) {
		t.Fatalf("round error = %v", err)
	}

	p = base
	p.Type = "UNKNOWN"
	if _, err := wager.NewExternal(p); !errors.Is(err, wager.ErrInvalidType) {
		t.Fatalf("type error = %v", err)
	}

	p = base
	neg, _ := money.New(-1, "BRL")
	p.Amount = neg
	if _, err := wager.NewExternal(p); !errors.Is(err, wager.ErrInvalidAmount) {
		t.Fatalf("amount error = %v", err)
	}
}

func TestValidTransitionsFromPending(t *testing.T) {
	now := time.Date(2026, 10, 6, 13, 0, 0, 0, time.UTC)

	cases := []struct {
		name  string
		apply func(*wager.Transaction) error
		want  wager.State
		code  wager.FailureCode
	}{
		{"processed", func(tx *wager.Transaction) error { return tx.MarkProcessed(now) }, wager.StateProcessed, ""},
		{"rejected", func(tx *wager.Transaction) error { return tx.MarkRejected("INSUFFICIENT_FUNDS", now) }, wager.StateRejected, "INSUFFICIENT_FUNDS"},
		{"failed", func(tx *wager.Transaction) error { return tx.MarkFailed("INFRA", now) }, wager.StateFailed, "INFRA"},
		{"pending_reference", func(tx *wager.Transaction) error { return tx.MarkPendingReference(now) }, wager.StatePendingReference, ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tx, err := wager.NewExternal(validExternal(t, wager.TypeBet))
			if err != nil {
				t.Fatalf("NewExternal() error = %v", err)
			}
			if err := tc.apply(&tx); err != nil {
				t.Fatalf("transition error = %v", err)
			}
			if tx.State() != tc.want {
				t.Fatalf("state = %s, want %s", tx.State(), tc.want)
			}
			if tx.FailureCode() != tc.code {
				t.Fatalf("failureCode = %s, want %s", tx.FailureCode(), tc.code)
			}
			if !tx.UpdatedAt().Equal(now) {
				t.Fatalf("updatedAt = %v, want %v", tx.UpdatedAt(), now)
			}
		})
	}
}

func TestValidTransitionsFromPendingReference(t *testing.T) {
	now := time.Date(2026, 10, 6, 14, 0, 0, 0, time.UTC)
	tx, _ := wager.NewExternal(validExternal(t, wager.TypeRefund))
	if err := tx.MarkPendingReference(now); err != nil {
		t.Fatalf("MarkPendingReference() error = %v", err)
	}

	for _, apply := range []struct {
		name string
		fn   func() error
		want wager.State
	}{
		{"processed", func() error { return tx.MarkProcessed(now) }, wager.StateProcessed},
	} {
		t.Run(apply.name, func(t *testing.T) {
			local, _ := wager.NewExternal(validExternal(t, wager.TypeRefund))
			_ = local.MarkPendingReference(now)
			if err := local.MarkProcessed(now); err != nil {
				t.Fatalf("error = %v", err)
			}
			if local.State() != wager.StateProcessed {
				t.Fatalf("state = %s", local.State())
			}
		})
	}

	localRejected, _ := wager.NewExternal(validExternal(t, wager.TypeRollback))
	_ = localRejected.MarkPendingReference(now)
	if err := localRejected.MarkRejected("REFERENCE_NOT_FOUND", now); err != nil {
		t.Fatalf("rejected error = %v", err)
	}
	if localRejected.State() != wager.StateRejected {
		t.Fatalf("state = %s", localRejected.State())
	}

	localFailed, _ := wager.NewExternal(validExternal(t, wager.TypeRollback))
	_ = localFailed.MarkPendingReference(now)
	if err := localFailed.MarkFailed("INFRA", now); err != nil {
		t.Fatalf("failed error = %v", err)
	}
	if localFailed.State() != wager.StateFailed {
		t.Fatalf("state = %s", localFailed.State())
	}
}

func TestInvalidTransitionsAndTerminalImmutability(t *testing.T) {
	now := time.Date(2026, 10, 6, 15, 0, 0, 0, time.UTC)

	processed, _ := wager.NewExternal(validExternal(t, wager.TypeBet))
	_ = processed.MarkProcessed(now)
	if err := processed.MarkRejected("X", now); !errors.Is(err, wager.ErrTerminalState) {
		t.Fatalf("processed->rejected error = %v", err)
	}
	if err := processed.MarkProcessed(now); !errors.Is(err, wager.ErrTerminalState) {
		t.Fatalf("processed->processed error = %v", err)
	}

	rejected, _ := wager.NewExternal(validExternal(t, wager.TypeBet))
	_ = rejected.MarkRejected("X", now)
	if err := rejected.MarkProcessed(now); !errors.Is(err, wager.ErrTerminalState) {
		t.Fatalf("rejected->processed error = %v", err)
	}

	failed, _ := wager.NewExternal(validExternal(t, wager.TypeBet))
	_ = failed.MarkFailed("X", now)
	if err := failed.MarkProcessed(now); !errors.Is(err, wager.ErrTerminalState) {
		t.Fatalf("failed->processed error = %v", err)
	}

	pendingRef, _ := wager.NewExternal(validExternal(t, wager.TypeRefund))
	_ = pendingRef.MarkPendingReference(now)
	if err := pendingRef.MarkPendingReference(now); !errors.Is(err, wager.ErrInvalidTransition) {
		t.Fatalf("pending_reference->pending_reference error = %v", err)
	}
}

func TestTerminalStates(t *testing.T) {
	for _, state := range []wager.State{
		wager.StateProcessed, wager.StateRejected, wager.StateFailed,
	} {
		if !state.IsTerminal() {
			t.Fatalf("%s should be terminal", state)
		}
	}
	for _, state := range []wager.State{
		wager.StatePending, wager.StatePendingReference,
	} {
		if state.IsTerminal() {
			t.Fatalf("%s should not be terminal", state)
		}
	}
}
