package wallet_test

import (
	"errors"
	"math"
	"testing"

	"github.com/junglegaming/backend-challenge-go/internal/domain/money"
	"github.com/junglegaming/backend-challenge-go/internal/domain/wallet"
)

func mustMoney(t *testing.T, cents int64, currency string) money.Money {
	t.Helper()
	m, err := money.New(cents, currency)
	if err != nil {
		t.Fatalf("money.New() error = %v", err)
	}
	return m
}

func TestNewWallet(t *testing.T) {
	w, err := wallet.New("w1", "p1", "BRL", mustMoney(t, 10000, "BRL"))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if w.Version() != 1 {
		t.Fatalf("version = %d, want 1", w.Version())
	}
	if w.Balance().Cents() != 10000 {
		t.Fatalf("balance = %d", w.Balance().Cents())
	}
}

func TestCreditAndDebit(t *testing.T) {
	w, _ := wallet.New("w1", "p1", "BRL", mustMoney(t, 10000, "BRL"))

	if err := w.Credit(mustMoney(t, 2500, "BRL")); err != nil {
		t.Fatalf("Credit() error = %v", err)
	}
	if w.Balance().Cents() != 12500 || w.Version() != 2 {
		t.Fatalf("after credit balance=%d version=%d", w.Balance().Cents(), w.Version())
	}

	if err := w.Debit(mustMoney(t, 2500, "BRL")); err != nil {
		t.Fatalf("Debit() error = %v", err)
	}
	if w.Balance().Cents() != 10000 || w.Version() != 3 {
		t.Fatalf("after debit balance=%d version=%d", w.Balance().Cents(), w.Version())
	}
}

func TestMultipleCreditsAndDebits(t *testing.T) {
	w, _ := wallet.New("w1", "p1", "BRL", mustMoney(t, 0, "BRL"))
	_ = w.Credit(mustMoney(t, 1000, "BRL"))
	_ = w.Credit(mustMoney(t, 2000, "BRL"))
	_ = w.Debit(mustMoney(t, 500, "BRL"))
	if w.Balance().Cents() != 2500 || w.Version() != 4 {
		t.Fatalf("balance=%d version=%d", w.Balance().Cents(), w.Version())
	}
}

func TestExactDebitToZero(t *testing.T) {
	w, _ := wallet.New("w1", "p1", "BRL", mustMoney(t, 10000, "BRL"))
	if err := w.Debit(mustMoney(t, 10000, "BRL")); err != nil {
		t.Fatalf("Debit() error = %v", err)
	}
	if !w.Balance().IsZero() || w.Version() != 2 {
		t.Fatalf("balance=%d version=%d", w.Balance().Cents(), w.Version())
	}
}

func TestInsufficientFundsDoesNotChangeState(t *testing.T) {
	w, _ := wallet.New("w1", "p1", "BRL", mustMoney(t, 10000, "BRL"))
	err := w.Debit(mustMoney(t, 15000, "BRL"))
	if !errors.Is(err, wallet.ErrInsufficientFunds) {
		t.Fatalf("error = %v, want ErrInsufficientFunds", err)
	}
	if w.Balance().Cents() != 10000 || w.Version() != 1 {
		t.Fatalf("state changed: balance=%d version=%d", w.Balance().Cents(), w.Version())
	}
}

func TestCurrencyMismatchDoesNotChangeState(t *testing.T) {
	w, _ := wallet.New("w1", "p1", "BRL", mustMoney(t, 10000, "BRL"))
	err := w.Credit(mustMoney(t, 100, "USD"))
	if !errors.Is(err, wallet.ErrCurrencyMismatch) {
		t.Fatalf("error = %v", err)
	}
	if w.Version() != 1 || w.Balance().Cents() != 10000 {
		t.Fatal("state changed on currency mismatch")
	}
}

func TestNonPositiveAmountRejected(t *testing.T) {
	w, _ := wallet.New("w1", "p1", "BRL", mustMoney(t, 10000, "BRL"))
	if err := w.Credit(mustMoney(t, 0, "BRL")); !errors.Is(err, wallet.ErrNonPositiveAmount) {
		t.Fatalf("zero credit error = %v", err)
	}
	if err := w.Debit(mustMoney(t, -1, "BRL")); !errors.Is(err, wallet.ErrNonPositiveAmount) {
		t.Fatalf("negative debit error = %v", err)
	}
	if w.Version() != 1 {
		t.Fatal("version changed")
	}
}

func TestCreditOverflowDoesNotChangeState(t *testing.T) {
	w, _ := wallet.New("w1", "p1", "BRL", mustMoney(t, math.MaxInt64, "BRL"))
	err := w.Credit(mustMoney(t, 1, "BRL"))
	if !errors.Is(err, money.ErrOverflow) {
		t.Fatalf("error = %v, want money.ErrOverflow", err)
	}
	if w.Version() != 1 || w.Balance().Cents() != math.MaxInt64 {
		t.Fatal("state changed on overflow")
	}
}

func TestRehydrate(t *testing.T) {
	w, err := wallet.Rehydrate("w1", "p1", "BRL", mustMoney(t, 5000, "BRL"), 7)
	if err != nil {
		t.Fatalf("Rehydrate() error = %v", err)
	}
	if w.Version() != 7 || w.Balance().Cents() != 5000 {
		t.Fatalf("rehydrated wallet incorrect: version=%d balance=%d", w.Version(), w.Balance().Cents())
	}
}

func TestRehydrateRejectsInvalidVersionAndNegativeBalance(t *testing.T) {
	if _, err := wallet.Rehydrate("w1", "p1", "BRL", mustMoney(t, 100, "BRL"), 0); !errors.Is(err, wallet.ErrInvalidVersion) {
		t.Fatalf("version error = %v", err)
	}
	if _, err := wallet.New("w1", "p1", "BRL", mustMoney(t, -1, "BRL")); !errors.Is(err, wallet.ErrNegativeBalance) {
		t.Fatalf("negative balance error = %v", err)
	}
}

func TestNewValidation(t *testing.T) {
	bal := mustMoney(t, 0, "BRL")
	if _, err := wallet.New("", "p1", "BRL", bal); !errors.Is(err, wallet.ErrInvalidID) {
		t.Fatalf("id error = %v", err)
	}
	if _, err := wallet.New("w1", "", "BRL", bal); !errors.Is(err, wallet.ErrInvalidPlayerID) {
		t.Fatalf("player error = %v", err)
	}
	if _, err := wallet.New("w1", "p1", "brl", bal); !errors.Is(err, wallet.ErrInvalidCurrency) {
		t.Fatalf("currency error = %v", err)
	}
	if _, err := wallet.New("w1", "p1", "BRL", mustMoney(t, 0, "USD")); !errors.Is(err, wallet.ErrBalanceCurrencyMismatch) {
		t.Fatalf("balance currency error = %v", err)
	}
}

func TestVersionOverflowDoesNotChangeState(t *testing.T) {
	for _, credit := range []bool{true, false} {
		w, err := wallet.Rehydrate("w1", "p1", "BRL", mustMoney(t, 100, "BRL"), math.MaxInt64)
		if err != nil {
			t.Fatal(err)
		}
		original := w
		if credit {
			err = w.Credit(mustMoney(t, 1, "BRL"))
		} else {
			err = w.Debit(mustMoney(t, 1, "BRL"))
		}
		if !errors.Is(err, wallet.ErrVersionOverflow) || w != original {
			t.Fatalf("credit=%v wallet=%v error=%v", credit, w, err)
		}
	}
}
