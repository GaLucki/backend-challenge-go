package money

import (
	"math"
	"strings"
)

// ParseDecimal converts a decimal amount string into Money without using floating point.
// Accepted forms: "25", "25.0", "25.00", "0", "0.00".
// Rejects scientific notation, NaN/Infinity, more than 2 fractional digits, and negatives.
func ParseDecimal(amount string, currency string) (Money, error) {
	normalizedCurrency, err := normalizeCurrency(currency)
	if err != nil {
		return Money{}, err
	}

	raw := strings.TrimSpace(amount)
	if raw == "" {
		return Money{}, ErrInvalidAmount
	}

	upper := strings.ToUpper(raw)
	switch upper {
	case "NAN", "INF", "+INF", "-INF", "INFINITY", "+INFINITY", "-INFINITY":
		return Money{}, ErrInvalidAmount
	}

	for i := 0; i < len(raw); i++ {
		if raw[i] == 'e' || raw[i] == 'E' {
			return Money{}, ErrInvalidAmount
		}
	}

	negative := false
	switch raw[0] {
	case '+':
		raw = raw[1:]
	case '-':
		negative = true
		raw = raw[1:]
	}
	if raw == "" {
		return Money{}, ErrInvalidAmount
	}

	wholePart, fracPart, err := splitDecimal(raw)
	if err != nil {
		return Money{}, err
	}

	if negative {
		return Money{}, ErrNegativeAmount
	}

	cents, err := combineToCents(wholePart, fracPart)
	if err != nil {
		return Money{}, err
	}

	return Money{cents: cents, currency: normalizedCurrency}, nil
}

func splitDecimal(raw string) (whole string, frac string, err error) {
	dot := strings.IndexByte(raw, '.')
	if dot == -1 {
		if !isDigits(raw) {
			return "", "", ErrInvalidAmount
		}
		return raw, "00", nil
	}

	whole = raw[:dot]
	frac = raw[dot+1:]
	if whole == "" || !isDigits(whole) {
		return "", "", ErrInvalidAmount
	}
	if frac == "" || strings.ContainsRune(frac, '.') {
		return "", "", ErrInvalidAmount
	}
	if len(frac) > 2 {
		return "", "", ErrInvalidAmount
	}
	if !isDigits(frac) {
		return "", "", ErrInvalidAmount
	}
	if len(frac) == 1 {
		frac += "0"
	}

	return whole, frac, nil
}

func combineToCents(wholePart, fracPart string) (int64, error) {
	whole, err := parseUint64(wholePart)
	if err != nil {
		return 0, err
	}
	frac, err := parseUint64(fracPart)
	if err != nil {
		return 0, err
	}

	if whole > uint64(math.MaxInt64)/100 {
		return 0, ErrOverflow
	}
	wholeCents := whole * 100
	if wholeCents > uint64(math.MaxInt64)-frac {
		return 0, ErrOverflow
	}
	return int64(wholeCents + frac), nil
}

func parseUint64(digits string) (uint64, error) {
	if digits == "" || !isDigits(digits) {
		return 0, ErrInvalidAmount
	}
	var value uint64
	for i := 0; i < len(digits); i++ {
		d := uint64(digits[i] - '0')
		if value > (math.MaxUint64-d)/10 {
			return 0, ErrOverflow
		}
		value = value*10 + d
	}
	return value, nil
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}
