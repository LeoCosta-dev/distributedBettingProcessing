package wallet

import (
	"errors"
	"github.com/leonardodacosta/distributedBettingProcessing/internal/domain/money"
	"testing"
)

func TestWalletBalanceAndVersionInvariants(t *testing.T) {
	opening, _ := money.New("100.00", "BRL")
	bet, _ := money.New("80.00", "BRL")
	w, err := New("w1", "p1", opening)
	if err != nil || w.ID() != "w1" || w.PlayerID() != "p1" || w.Version() != 1 {
		t.Fatal(err)
	}
	if err := w.Debit(bet); err != nil || w.Balance().Minor() != 2000 || w.Version() != 2 {
		t.Fatalf("unexpected wallet: %v", err)
	}
	if err := w.Debit(bet); !errors.Is(err, ErrInsufficientBalance) || w.Balance().Minor() != 2000 || w.Version() != 2 {
		t.Fatalf("invalid debit mutated wallet: %v", err)
	}
	credit, _ := money.New("5.00", "BRL")
	if err := w.Credit(credit); err != nil || w.Version() != 3 {
		t.Fatal(err)
	}
}

func TestWalletStateCannotBeChangedThroughPublicAPI(t *testing.T) {
	opening, _ := money.New("10.00", "BRL")
	w, _ := New("w", "p", opening)
	snapshot := w
	zero, _ := money.Zero("BRL")
	if err := w.Debit(zero); err != nil {
		t.Fatal(err)
	}
	if w.Version() != snapshot.Version() || w.Balance() != snapshot.Balance() {
		t.Fatal("zero debit changed state")
	}
}
