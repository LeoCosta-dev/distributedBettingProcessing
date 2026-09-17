package ledger

import (
	"errors"
	"github.com/leonardodacosta/distributedBettingProcessing/internal/domain/money"
	"time"
)

var (
	ErrInvalidEntry     = errors.New("invalid ledger entry")
	ErrInvalidBalance   = errors.New("invalid ledger balance")
	ErrCurrencyMismatch = errors.New("ledger currency mismatch")
)

type Direction string

const (
	Credit Direction = "CREDIT"
	Debit  Direction = "DEBIT"
)

type Entry struct {
	id, walletID, transactionID        string
	direction                          Direction
	value, balanceBefore, balanceAfter money.Money
	timestamp                          time.Time
}

func New(id, walletID, transactionID string, direction Direction, value, balanceBefore, balanceAfter money.Money, timestamp time.Time) (Entry, error) {
	if id == "" || walletID == "" || transactionID == "" || timestamp.IsZero() || (direction != Credit && direction != Debit) || value.Minor() <= 0 {
		return Entry{}, ErrInvalidEntry
	}
	if value.Currency() == "" || balanceBefore.Currency() == "" || balanceAfter.Currency() == "" || value.Currency() != balanceBefore.Currency() || value.Currency() != balanceAfter.Currency() {
		return Entry{}, ErrCurrencyMismatch
	}
	if balanceBefore.Minor() < 0 || balanceAfter.Minor() < 0 {
		return Entry{}, ErrInvalidBalance
	}
	var expected money.Money
	var err error
	if direction == Credit {
		expected, err = balanceBefore.Add(value)
	} else {
		expected, err = balanceBefore.Sub(value)
	}
	if err != nil {
		return Entry{}, err
	}
	if expected.Minor() != balanceAfter.Minor() {
		return Entry{}, ErrInvalidBalance
	}
	return Entry{id: id, walletID: walletID, transactionID: transactionID, direction: direction, value: value, balanceBefore: balanceBefore, balanceAfter: balanceAfter, timestamp: timestamp}, nil
}
func (e Entry) ID() string                 { return e.id }
func (e Entry) WalletID() string           { return e.walletID }
func (e Entry) TransactionID() string      { return e.transactionID }
func (e Entry) Direction() Direction       { return e.direction }
func (e Entry) Value() money.Money         { return e.value }
func (e Entry) BalanceBefore() money.Money { return e.balanceBefore }
func (e Entry) BalanceAfter() money.Money  { return e.balanceAfter }
func (e Entry) Timestamp() time.Time       { return e.timestamp }
