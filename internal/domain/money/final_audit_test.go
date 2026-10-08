package money_test

import (
	"errors"
	"math"
	"testing"

	"github.com/junglegaming/backend-challenge-go/internal/domain/money"
)

func TestSubtractRepresentableMinInt64DoesNotRequireNegation(t *testing.T) {
	for _, test := range []struct{ a, b, want int64 }{
		{math.MinInt64, math.MinInt64, 0},
		{-1, math.MinInt64, math.MaxInt64},
		{math.MinInt64, -1, math.MinInt64 + 1},
	} {
		a, _ := money.New(test.a, "BRL")
		b, _ := money.New(test.b, "BRL")
		result, err := a.Sub(b)
		if err != nil || result.Cents() != test.want {
			t.Fatalf("%d - %d: %v, %v", test.a, test.b, result, err)
		}
	}
}

func TestInvalidMoneyCannotNegateOrCompareEqual(t *testing.T) {
	var invalid money.Money
	if _, err := invalid.Negate(); !errors.Is(err, money.ErrInvalidCurrency) {
		t.Fatal(err)
	}
	if invalid.Equal(invalid) {
		t.Fatal("uninitialized money compares as a valid value")
	}
}

func TestSupportedCurrencyIsMoreThanThreeUppercaseLetters(t *testing.T) {
	for _, currency := range []string{"XYZ", "ZZZ", "AAA"} {
		if _, err := money.New(0, currency); !errors.Is(err, money.ErrInvalidCurrency) {
			t.Fatal("accepted unsupported currency", currency)
		}
	}
}
