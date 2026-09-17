package money

import (
	"errors"
	"fmt"
	"math"
	"testing"
)

func TestMoneyParsingAndJSON(t *testing.T) {
	m, err := New("25.00", "BRL")
	if err != nil {
		t.Fatal(err)
	}
	if m.Minor() != 2500 || m.Amount() != "25.00" {
		t.Fatalf("unexpected money: %#v", m)
	}
	b, err := m.MarshalJSON()
	if err != nil || string(b) != `{"amount":"25.00","currency":"BRL"}` {
		t.Fatalf("unexpected JSON: %s (%v)", b, err)
	}
	var decoded Money
	if err := decoded.UnmarshalJSON(b); err != nil || decoded != m {
		t.Fatalf("unexpected decoded money: %#v (%v)", decoded, err)
	}
}

func TestMoneyRejectsInvalidExternalAmounts(t *testing.T) {
	for _, input := range []string{
		"25", "1", "0", "25.0", "25.000", "25.001",
		"-1.00", "1e2", "01.00", "", "abc",
	} {
		if _, err := New(input, "BRL"); !errors.Is(err, ErrInvalidAmount) {
			t.Errorf("%q: got %v", input, err)
		}
	}
}

func TestMoneyParsingPreservesEveryCent(t *testing.T) {
	for _, whole := range []int{0, 1, 25} {
		for cent := 0; cent < 100; cent++ {
			input := fmt.Sprintf("%d.%02d", whole, cent)
			want := int64(whole*100 + cent)
			t.Run(input, func(t *testing.T) {
				got, err := New(input, "BRL")
				if err != nil {
					t.Fatal(err)
				}
				if got.Minor() != want || got.Amount() != input {
					t.Fatalf("%q: minor=%d amount=%q, want minor=%d amount=%q", input, got.Minor(), got.Amount(), want, input)
				}
			})
		}
	}
}

func TestMoneyArithmeticCurrencyAndOverflow(t *testing.T) {
	a, _ := New("2.00", "BRL")
	b, _ := New("3.00", "BRL")
	usd, _ := New("1.00", "USD")
	if got, _ := a.Add(b); got.Minor() != 500 {
		t.Fatal(got)
	}
	if _, err := a.Add(usd); !errors.Is(err, ErrCurrencyMismatch) {
		t.Fatal(err)
	}
	max := Money{minor: math.MaxInt64, currency: "BRL"}
	if _, err := max.Add(a); !errors.Is(err, ErrOverflow) {
		t.Fatal(err)
	}
	min := Money{minor: math.MinInt64, currency: "BRL"}
	if _, err := min.Negate(); !errors.Is(err, ErrOverflow) {
		t.Fatal(err)
	}
}
