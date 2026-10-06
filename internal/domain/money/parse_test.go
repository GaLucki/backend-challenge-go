package money_test

import (
	"errors"
	"testing"

	"github.com/junglegaming/backend-challenge-go/internal/domain/money"
)

func TestParseDecimalAcceptsValidForms(t *testing.T) {
	cases := []struct {
		amount string
		cents  int64
	}{
		{"25", 2500},
		{"25.0", 2500},
		{"25.00", 2500},
		{"25.5", 2550},
		{"25.50", 2550},
		{"0", 0},
		{"0.00", 0},
		{"0.0", 0},
		{"1.01", 101},
	}

	for _, tc := range cases {
		m, err := money.ParseDecimal(tc.amount, "BRL")
		if err != nil {
			t.Fatalf("ParseDecimal(%q) error = %v", tc.amount, err)
		}
		if m.Cents() != tc.cents {
			t.Fatalf("ParseDecimal(%q) cents = %d, want %d", tc.amount, m.Cents(), tc.cents)
		}
	}
}

func TestParseDecimalRejectsInvalidForms(t *testing.T) {
	cases := []string{
		"",
		"abc",
		"25.001",
		"25.0001",
		"1e2",
		"1E2",
		"NaN",
		"Infinity",
		"-Infinity",
		"+Infinity",
		"INF",
		"25.00.1",
		"25.",
		".25",
		"--1",
		"+-1",
	}

	for _, amount := range cases {
		_, err := money.ParseDecimal(amount, "BRL")
		if err == nil {
			t.Fatalf("ParseDecimal(%q) succeeded, want error", amount)
		}
		if !errors.Is(err, money.ErrInvalidAmount) && !errors.Is(err, money.ErrNegativeAmount) {
			t.Fatalf("ParseDecimal(%q) error = %v, want ErrInvalidAmount or ErrNegativeAmount", amount, err)
		}
	}
}

func TestParseDecimalRejectsNegatives(t *testing.T) {
	if _, err := money.ParseDecimal("-25.00", "BRL"); !errors.Is(err, money.ErrNegativeAmount) {
		t.Fatalf("error = %v, want ErrNegativeAmount", err)
	}
}

func TestParseDecimalRejectsInvalidCurrency(t *testing.T) {
	if _, err := money.ParseDecimal("25.00", "brl"); !errors.Is(err, money.ErrInvalidCurrency) {
		t.Fatalf("error = %v, want ErrInvalidCurrency", err)
	}
}

func TestParseDecimalRoundTripRepresentation(t *testing.T) {
	m, err := money.ParseDecimal("25", "USD")
	if err != nil {
		t.Fatalf("ParseDecimal() error = %v", err)
	}
	if m.DecimalString() != "25.00" {
		t.Fatalf("got %q, want 25.00", m.DecimalString())
	}

	m, err = money.ParseDecimal("25.5", "EUR")
	if err != nil {
		t.Fatalf("ParseDecimal() error = %v", err)
	}
	if m.DecimalString() != "25.50" {
		t.Fatalf("got %q, want 25.50", m.DecimalString())
	}
}
