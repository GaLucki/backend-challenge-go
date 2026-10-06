package money

import (
	"fmt"
	"math"
)

// Money is an immutable monetary value represented in minor units (cents).
// Example: cents=2500 and currency="BRL" means 25.00 BRL.
type Money struct {
	cents    int64
	currency string
}

// External is the public decimal representation of Money.
// It is intentionally free of HTTP concerns; adapters may marshal it as JSON.
type External struct {
	Amount   string `json:"amount"`
	Currency string `json:"currency"`
}

// New creates Money from cents and an ISO 4217 currency code.
func New(cents int64, currency string) (Money, error) {
	normalized, err := normalizeCurrency(currency)
	if err != nil {
		return Money{}, err
	}
	return Money{cents: cents, currency: normalized}, nil
}

// Zero returns a zero-value Money for the given currency.
func Zero(currency string) (Money, error) {
	return New(0, currency)
}

// Cents returns the amount in minor units.
func (m Money) Cents() int64 {
	return m.cents
}

// Currency returns the ISO 4217 currency code.
func (m Money) Currency() string {
	return m.currency
}

// IsZero reports whether the amount is zero.
func (m Money) IsZero() bool {
	return m.cents == 0
}

// IsPositive reports whether the amount is greater than zero.
func (m Money) IsPositive() bool {
	return m.cents > 0
}

// IsNegative reports whether the amount is less than zero.
func (m Money) IsNegative() bool {
	return m.cents < 0
}

// Add returns m + other. Currencies must match. Overflow is detected.
func (m Money) Add(other Money) (Money, error) {
	if err := m.ensureSameCurrency(other); err != nil {
		return Money{}, err
	}

	cents, err := addCents(m.cents, other.cents)
	if err != nil {
		return Money{}, err
	}
	return Money{cents: cents, currency: m.currency}, nil
}

// Sub returns m - other. Currencies must match. Overflow/underflow are detected.
func (m Money) Sub(other Money) (Money, error) {
	if err := m.ensureSameCurrency(other); err != nil {
		return Money{}, err
	}

	negated, err := negateCents(other.cents)
	if err != nil {
		return Money{}, err
	}
	cents, err := addCents(m.cents, negated)
	if err != nil {
		return Money{}, err
	}
	return Money{cents: cents, currency: m.currency}, nil
}

// Negate returns -m. Overflow is detected for math.MinInt64.
func (m Money) Negate() (Money, error) {
	cents, err := negateCents(m.cents)
	if err != nil {
		return Money{}, err
	}
	return Money{cents: cents, currency: m.currency}, nil
}

// Compare returns -1 when m < other, 0 when equal, and 1 when m > other.
func (m Money) Compare(other Money) (int, error) {
	if err := m.ensureSameCurrency(other); err != nil {
		return 0, err
	}
	switch {
	case m.cents < other.cents:
		return -1, nil
	case m.cents > other.cents:
		return 1, nil
	default:
		return 0, nil
	}
}

// Equal reports whether both amounts and currencies are identical.
func (m Money) Equal(other Money) bool {
	return m.currency == other.currency && m.cents == other.cents
}

// DecimalString returns the fixed-scale external amount representation.
func (m Money) DecimalString() string {
	sign := ""
	cents := m.cents
	if cents < 0 {
		sign = "-"
		// MinInt64 cannot be negated; format from unsigned magnitude carefully.
		if cents == math.MinInt64 {
			return formatMinInt64Decimal()
		}
		cents = -cents
	}

	whole := cents / 100
	frac := cents % 100
	return fmt.Sprintf("%s%d.%02d", sign, whole, frac)
}

// External returns the public decimal representation.
func (m Money) External() External {
	return External{
		Amount:   m.DecimalString(),
		Currency: m.currency,
	}
}

func (m Money) ensureSameCurrency(other Money) error {
	if m.currency == "" || other.currency == "" {
		return ErrInvalidCurrency
	}
	if m.currency != other.currency {
		return ErrCurrencyMismatch
	}
	return nil
}

func normalizeCurrency(currency string) (string, error) {
	if len(currency) != 3 {
		return "", ErrInvalidCurrency
	}
	for i := 0; i < 3; i++ {
		c := currency[i]
		if c < 'A' || c > 'Z' {
			return "", ErrInvalidCurrency
		}
	}
	return currency, nil
}

func addCents(a, b int64) (int64, error) {
	if b > 0 && a > math.MaxInt64-b {
		return 0, ErrOverflow
	}
	if b < 0 && a < math.MinInt64-b {
		return 0, ErrUnderflow
	}
	return a + b, nil
}

func negateCents(v int64) (int64, error) {
	if v == math.MinInt64 {
		return 0, ErrOverflow
	}
	return -v, nil
}

func formatMinInt64Decimal() string {
	// math.MinInt64 = -9223372036854775808
	// in cents => -92233720368547758.08
	const minAbsWithoutSign = "92233720368547758.08"
	return "-" + minAbsWithoutSign
}
