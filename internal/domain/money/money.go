package money

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
)

var (
	ErrInvalidAmount    = errors.New("invalid monetary amount")
	ErrInvalidCurrency  = errors.New("invalid currency")
	ErrCurrencyMismatch = errors.New("currency mismatch")
	ErrOverflow         = errors.New("monetary overflow")
	amountPattern       = regexp.MustCompile(`^(0|[1-9][0-9]*)(\.[0-9]{2})?$`)
	currencyPattern     = regexp.MustCompile(`^[A-Z]{3}$`)
)

// Money stores an exact amount in the smallest currency unit (cents).
type Money struct {
	minor    int64
	currency string
}

func New(amount string, currency string) (Money, error) {
	if !currencyPattern.MatchString(currency) {
		return Money{}, ErrInvalidCurrency
	}
	if !amountPattern.MatchString(amount) {
		return Money{}, ErrInvalidAmount
	}
	// Parse without floating point and reject values outside int64.
	wholePart, fraction := amount, int64(0)
	if dot := indexByte(amount, '.'); dot >= 0 {
		wholePart = amount[:dot]
		var f int64
		_, _ = fmt.Sscan(amount[dot+1:], &f)
		fraction = f
	}
	if wholePart == "" {
		return Money{}, ErrInvalidAmount
	}
	var parsed int64
	for _, digit := range []byte(wholePart) {
		if digit < '0' || digit > '9' || parsed > (math.MaxInt64-int64(digit-'0'))/10 {
			return Money{}, ErrOverflow
		}
		parsed = parsed*10 + int64(digit-'0')
	}
	if parsed > (math.MaxInt64-fraction)/100 {
		return Money{}, ErrOverflow
	}
	return Money{minor: parsed*100 + fraction, currency: currency}, nil
}

func Zero(currency string) (Money, error) { return New("0.00", currency) }

func (m Money) Minor() int64     { return m.minor }
func (m Money) Currency() string { return m.currency }

func (m Money) Add(other Money) (Money, error) {
	if err := m.compatible(other); err != nil {
		return Money{}, err
	}
	if (other.minor > 0 && m.minor > math.MaxInt64-other.minor) ||
		(other.minor < 0 && m.minor < math.MinInt64-other.minor) {
		return Money{}, ErrOverflow
	}
	return Money{minor: m.minor + other.minor, currency: m.currency}, nil
}

func (m Money) Sub(other Money) (Money, error) {
	if err := m.compatible(other); err != nil {
		return Money{}, err
	}
	if (other.minor > 0 && m.minor < math.MinInt64+other.minor) ||
		(other.minor < 0 && m.minor > math.MaxInt64+other.minor) {
		return Money{}, ErrOverflow
	}
	return Money{minor: m.minor - other.minor, currency: m.currency}, nil
}

func (m Money) Negate() (Money, error) {
	if m.minor == math.MinInt64 {
		return Money{}, ErrOverflow
	}
	return Money{minor: -m.minor, currency: m.currency}, nil
}

func (m Money) Compare(other Money) (int, error) {
	if err := m.compatible(other); err != nil {
		return 0, err
	}
	if m.minor < other.minor {
		return -1, nil
	}
	if m.minor > other.minor {
		return 1, nil
	}
	return 0, nil
}

func (m Money) String() string {
	return fmt.Sprintf("%s %s", m.Amount(), m.currency)
}
func (m Money) Amount() string {
	if m.minor < 0 {
		if m.minor == math.MinInt64 {
			return "-92233720368547758.08"
		}
		minor := -m.minor
		return fmt.Sprintf("-%d.%02d", minor/100, minor%100)
	}
	return fmt.Sprintf("%d.%02d", m.minor/100, m.minor%100)
}

func (m Money) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Amount   string `json:"amount"`
		Currency string `json:"currency"`
	}{m.Amount(), m.currency})
}

func (m *Money) UnmarshalJSON(data []byte) error {
	var value struct {
		Amount   string `json:"amount"`
		Currency string `json:"currency"`
	}
	if err := json.Unmarshal(data, &value); err != nil {
		return ErrInvalidAmount
	}
	parsed, err := New(value.Amount, value.Currency)
	if err != nil {
		return err
	}
	*m = parsed
	return nil
}

func (m Money) compatible(other Money) error {
	if m.currency == "" || other.currency == "" {
		return ErrInvalidCurrency
	}
	if m.currency != other.currency {
		return ErrCurrencyMismatch
	}
	return nil
}
func indexByte(s string, b byte) int {
	for i := range s {
		if s[i] == b {
			return i
		}
	}
	return -1
}
