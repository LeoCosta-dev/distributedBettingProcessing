package wager

import (
	"errors"
	"github.com/leonardodacosta/distributedBettingProcessing/internal/domain/money"
)

var (
	ErrInvalidTransaction = errors.New("invalid wagering transaction")
	ErrInvalidTransition  = errors.New("invalid transaction transition")
)

type Type string

const (
	Opening  Type = "OPENING"
	Bet      Type = "BET"
	Win      Type = "WIN"
	Loss     Type = "LOSS"
	Refund   Type = "REFUND"
	Rollback Type = "ROLLBACK"
)

type State string

const (
	Pending          State = "PENDING"
	Processed        State = "PROCESSED"
	Rejected         State = "REJECTED"
	Failed           State = "FAILED"
	PendingReference State = "PENDING_REFERENCE"
)

type Transaction struct {
	id, externalID, providerID, walletID string
	typ                                  Type
	amount                               money.Money
	state                                State
}

func New(id, externalID, providerID, walletID string, typ Type, amount money.Money) (Transaction, error) {
	if id == "" || externalID == "" || providerID == "" || walletID == "" || !validExternalType(typ) || !validAmount(typ, amount) {
		return Transaction{}, ErrInvalidTransaction
	}
	return Transaction{id: id, externalID: externalID, providerID: providerID, walletID: walletID, typ: typ, amount: amount, state: Pending}, nil
}
func NewOpening(id, walletID string, amount money.Money) (Transaction, error) {
	if id == "" || walletID == "" || amount.Minor() <= 0 || amount.Currency() == "" {
		return Transaction{}, ErrInvalidTransaction
	}
	return Transaction{id: id, externalID: id, providerID: "internal", walletID: walletID, typ: Opening, amount: amount, state: Pending}, nil
}
func (t Transaction) ID() string          { return t.id }
func (t Transaction) ExternalID() string  { return t.externalID }
func (t Transaction) ProviderID() string  { return t.providerID }
func (t Transaction) WalletID() string    { return t.walletID }
func (t Transaction) Type() Type          { return t.typ }
func (t Transaction) Amount() money.Money { return t.amount }
func (t Transaction) State() State        { return t.state }
func validExternalType(typ Type) bool {
	switch typ {
	case Bet, Win, Loss, Refund, Rollback:
		return true
	default:
		return false
	}
}
func validAmount(typ Type, amount money.Money) bool {
	if amount.Currency() == "" || amount.Minor() < 0 {
		return false
	}
	if typ == Loss {
		return amount.Minor() == 0
	}
	return amount.Minor() > 0
}
func (t *Transaction) Transition(next State) error {
	if t.state == Pending {
		switch next {
		case Processed, Rejected, Failed, PendingReference:
			t.state = next
			return nil
		}
	}
	if t.state == PendingReference && (next == Processed || next == Rejected) {
		t.state = next
		return nil
	}
	return ErrInvalidTransition
}
