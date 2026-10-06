package money_test

import (
	"errors"
	"math"
	"testing"

	"github.com/junglegaming/backend-challenge-go/internal/domain/money"
)

func TestNewAcceptsCurrencies(t *testing.T) {
	for _, currency := range []string{"BRL", "USD", "EUR"} {
		m, err := money.New(2500, currency)
		if err != nil {
			t.Fatalf("New(%q) error = %v", currency, err)
		}
		if m.Cents() != 2500 || m.Currency() != currency {
			t.Fatalf("New(%q) = %+v", currency, m)
		}
	}
}

func TestNewRejectsInvalidCurrency(t *testing.T) {
	cases := []string{"", "brl", "BR", "BRLA", "BR1", "123"}
	for _, currency := range cases {
		if _, err := money.New(0, currency); !errors.Is(err, money.ErrInvalidCurrency) {
			t.Fatalf("New(%q) error = %v, want ErrInvalidCurrency", currency, err)
		}
	}
}

func TestZero(t *testing.T) {
	m, err := money.Zero("BRL")
	if err != nil {
		t.Fatalf("Zero() error = %v", err)
	}
	if !m.IsZero() || m.IsPositive() || m.IsNegative() {
		t.Fatalf("unexpected zero flags: %+v", m)
	}
}

func TestAddSubCompareEqual(t *testing.T) {
	a, _ := money.New(10000, "BRL")
	b, _ := money.New(2500, "BRL")

	sum, err := a.Add(b)
	if err != nil {
		t.Fatalf("Add() error = %v", err)
	}
	if sum.Cents() != 12500 {
		t.Fatalf("Add() cents = %d, want 12500", sum.Cents())
	}

	diff, err := a.Sub(b)
	if err != nil {
		t.Fatalf("Sub() error = %v", err)
	}
	if diff.Cents() != 7500 {
		t.Fatalf("Sub() cents = %d, want 7500", diff.Cents())
	}

	cmp, err := a.Compare(b)
	if err != nil || cmp != 1 {
		t.Fatalf("Compare() = %d, %v", cmp, err)
	}
	if !a.Equal(a) || a.Equal(b) {
		t.Fatal("Equal checks failed")
	}
}

func TestCurrencyMismatch(t *testing.T) {
	brl, _ := money.New(100, "BRL")
	usd, _ := money.New(100, "USD")

	if _, err := brl.Add(usd); !errors.Is(err, money.ErrCurrencyMismatch) {
		t.Fatalf("Add mismatch error = %v", err)
	}
	if _, err := brl.Sub(usd); !errors.Is(err, money.ErrCurrencyMismatch) {
		t.Fatalf("Sub mismatch error = %v", err)
	}
	if _, err := brl.Compare(usd); !errors.Is(err, money.ErrCurrencyMismatch) {
		t.Fatalf("Compare mismatch error = %v", err)
	}
	if brl.Equal(usd) {
		t.Fatal("Equal should be false for different currencies")
	}
}

func TestPositiveAndNegative(t *testing.T) {
	positive, _ := money.New(1, "BRL")
	negative, _ := money.New(-1, "BRL")

	if !positive.IsPositive() || positive.IsNegative() {
		t.Fatal("positive flags incorrect")
	}
	if !negative.IsNegative() || negative.IsPositive() {
		t.Fatal("negative flags incorrect")
	}

	negated, err := positive.Negate()
	if err != nil {
		t.Fatalf("Negate() error = %v", err)
	}
	if negated.Cents() != -1 {
		t.Fatalf("Negate() = %d", negated.Cents())
	}
}

func TestOverflowAndUnderflow(t *testing.T) {
	max, _ := money.New(math.MaxInt64, "BRL")
	one, _ := money.New(1, "BRL")
	if _, err := max.Add(one); !errors.Is(err, money.ErrOverflow) {
		t.Fatalf("max+1 error = %v, want ErrOverflow", err)
	}

	min, _ := money.New(math.MinInt64, "BRL")
	if _, err := min.Sub(one); !errors.Is(err, money.ErrUnderflow) {
		t.Fatalf("min-1 error = %v, want ErrUnderflow", err)
	}
	if _, err := min.Negate(); !errors.Is(err, money.ErrOverflow) {
		t.Fatalf("negate min error = %v, want ErrOverflow", err)
	}
}

func TestDecimalString(t *testing.T) {
	cases := []struct {
		cents int64
		want  string
	}{
		{2500, "25.00"},
		{2550, "25.50"},
		{0, "0.00"},
		{5, "0.05"},
		{100, "1.00"},
		{-2500, "-25.00"},
	}
	for _, tc := range cases {
		m, err := money.New(tc.cents, "BRL")
		if err != nil {
			t.Fatalf("New(%d) error = %v", tc.cents, err)
		}
		if got := m.DecimalString(); got != tc.want {
			t.Fatalf("DecimalString(%d) = %q, want %q", tc.cents, got, tc.want)
		}
	}
}

func TestExternalRepresentation(t *testing.T) {
	m, _ := money.New(2500, "BRL")
	ext := m.External()
	if ext.Amount != "25.00" || ext.Currency != "BRL" {
		t.Fatalf("External() = %+v", ext)
	}
}
