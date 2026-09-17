package wager

import (
	"errors"
	"github.com/leonardodacosta/distributedBettingProcessing/internal/domain/money"
	"testing"
)

func TestTransactionValueRules(t *testing.T) {
	zero, _ := money.Zero("BRL")
	one, _ := money.New("1.00", "BRL")
	cases := []struct {
		name   string
		typ    Type
		amount money.Money
		valid  bool
	}{
		{"loss zero", Loss, zero, true}, {"loss nonzero", Loss, one, false}, {"bet zero", Bet, zero, false}, {"win zero", Win, zero, false}, {"refund zero", Refund, zero, false}, {"rollback zero", Rollback, zero, false}, {"bet positive", Bet, one, true}, {"win positive", Win, one, true}, {"refund positive", Refund, one, true}, {"rollback positive", Rollback, one, true}, {"opening", Opening, one, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := New("id", "ext", "provider", "wallet", tc.typ, tc.amount)
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v, err=%v", tc.valid, err)
			}
		})
	}
}

func TestTransactionTransitions(t *testing.T) {
	one, _ := money.New("1.00", "BRL")
	cases := []struct {
		from, to State
		allowed  bool
	}{
		{Pending, Processed, true}, {Pending, Rejected, true}, {Pending, Failed, true}, {Pending, PendingReference, true}, {Pending, Pending, false},
		{PendingReference, Processed, true}, {PendingReference, Rejected, true}, {PendingReference, Failed, false}, {PendingReference, Pending, false},
		{Processed, Processed, false}, {Processed, Rejected, false}, {Rejected, Processed, false}, {Failed, Processed, false},
	}
	for _, tc := range cases {
		t.Run(string(tc.from)+"_to_"+string(tc.to), func(t *testing.T) {
			tx, _ := New("id", "ext", "provider", "wallet", Bet, one)
			tx.state = tc.from
			err := tx.Transition(tc.to)
			if (err == nil) != tc.allowed {
				t.Fatalf("allowed=%v, err=%v", tc.allowed, err)
			}
		})
	}
}

func TestTransactionStateCannotBeChangedThroughPublicAPI(t *testing.T) {
	one, _ := money.New("1.00", "BRL")
	tx, _ := New("id", "ext", "provider", "wallet", Bet, one)
	if tx.State() != Pending {
		t.Fatal(tx.State())
	}
	if tx.Transition(Processed) != nil || tx.State() != Processed {
		t.Fatal(tx.State())
	}
	if !errors.Is(tx.Transition(Rejected), ErrInvalidTransition) {
		t.Fatal("terminal state changed")
	}
}
