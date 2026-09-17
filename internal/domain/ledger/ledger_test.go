package ledger

import (
	"errors"
	"github.com/leonardodacosta/distributedBettingProcessing/internal/domain/money"
	"testing"
	"time"
)

func TestLedgerDirectionsAndBalances(t *testing.T) {
	one, _ := money.New("1.00", "BRL")
	before, _ := money.New("10.00", "BRL")
	afterCredit, _ := money.New("11.00", "BRL")
	afterDebit, _ := money.New("9.00", "BRL")
	for _, tc := range []struct {
		direction Direction
		after     money.Money
	}{{Credit, afterCredit}, {Debit, afterDebit}} {
		t.Run(string(tc.direction), func(t *testing.T) {
			e, err := New("e", "w", "t", tc.direction, one, before, tc.after, time.Unix(1, 0))
			if err != nil || e.Direction() != tc.direction || e.BalanceAfter() != tc.after {
				t.Fatal(err)
			}
		})
	}
}

func TestLedgerRejectsInconsistentAndMismatchedEntries(t *testing.T) {
	one, _ := money.New("1.00", "BRL")
	before, _ := money.New("10.00", "BRL")
	wrong, _ := money.New("12.00", "BRL")
	usd, _ := money.New("1.00", "USD")
	if _, err := New("e", "w", "t", Credit, one, before, wrong, time.Unix(1, 0)); !errors.Is(err, ErrInvalidBalance) {
		t.Fatal(err)
	}
	if _, err := New("e", "w", "t", Credit, usd, before, wrong, time.Unix(1, 0)); !errors.Is(err, ErrCurrencyMismatch) {
		t.Fatal(err)
	}
}

func TestLedgerEntryIsImmutableThroughPublicAPI(t *testing.T) {
	one, _ := money.New("1.00", "BRL")
	before, _ := money.New("10.00", "BRL")
	after, _ := money.New("11.00", "BRL")
	e, _ := New("e", "w", "t", Credit, one, before, after, time.Unix(1, 0))
	if e.ID() != "e" || e.Value() != one || e.BalanceBefore() != before {
		t.Fatal("unexpected entry")
	}
}
