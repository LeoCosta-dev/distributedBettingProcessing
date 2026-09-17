package wallet

import (
	"errors"

	"github.com/leonardodacosta/distributedBettingProcessing/internal/domain/money"
)

var (
	ErrInvalidWallet       = errors.New("invalid wallet")
	ErrInsufficientBalance = errors.New("insufficient balance")
)

type Wallet struct {
	id       string
	playerID string
	balance  money.Money
	version  uint64
}

func New(id, playerID string, opening money.Money) (Wallet, error) {
	if id == "" || playerID == "" || opening.Minor() < 0 || opening.Currency() == "" {
		return Wallet{}, ErrInvalidWallet
	}
	return Wallet{id: id, playerID: playerID, balance: opening, version: 1}, nil
}

func (w Wallet) ID() string           { return w.id }
func (w Wallet) PlayerID() string     { return w.playerID }
func (w Wallet) Balance() money.Money { return w.balance }
func (w Wallet) Version() uint64      { return w.version }

func (w *Wallet) Debit(amount money.Money) error {
	if amount.Minor() < 0 {
		return ErrInvalidWallet
	}
	if _, err := w.balance.Compare(amount); err != nil {
		return err
	}
	if amount.Minor() == 0 {
		return nil
	}
	if w.balance.Minor() < amount.Minor() {
		return ErrInsufficientBalance
	}
	next, err := w.balance.Sub(amount)
	if err != nil {
		return err
	}
	w.balance = next
	w.version++
	return nil
}

func (w *Wallet) Credit(amount money.Money) error {
	if amount.Minor() < 0 {
		return ErrInvalidWallet
	}
	if _, err := w.balance.Compare(amount); err != nil {
		return err
	}
	if amount.Minor() == 0 {
		return nil
	}
	next, err := w.balance.Add(amount)
	if err != nil {
		return err
	}
	w.balance = next
	w.version++
	return nil
}
