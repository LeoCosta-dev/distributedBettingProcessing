package financial

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/leonardodacosta/distributedBettingProcessing/internal/domain/ledger"
	"github.com/leonardodacosta/distributedBettingProcessing/internal/domain/money"
	"github.com/leonardodacosta/distributedBettingProcessing/internal/domain/wager"
	"github.com/leonardodacosta/distributedBettingProcessing/internal/domain/wallet"
	"github.com/leonardodacosta/distributedBettingProcessing/internal/infrastructure/postgres"
)

var (
	ErrInvalidCommand   = errors.New("invalid financial command")
	ErrReferenceInvalid = errors.New("invalid reference")
)

const legacyIdentity = "legacy"

type Command struct {
	ID, WalletID                                                                   uuid.UUID
	ExternalID, ProviderID, PlayerID, GameID, RoundID, IdempotencyKey, PayloadHash string
	ReferenceExternalID                                                            string
	Type                                                                           wager.Type
	Amount                                                                         money.Money
}

type Result struct {
	TransactionID uuid.UUID
	State         wager.State
	Balance       int64
	Amount        money.Money
}

type Reconciliation struct {
	WalletBalance, LedgerBalance int64
	Consistent                   bool
}

type eventPayload struct {
	TransactionID uuid.UUID   `json:"transactionId"`
	WalletID      uuid.UUID   `json:"walletId"`
	Type          wager.Type  `json:"type"`
	State         wager.State `json:"state"`
	Amount        money.Money `json:"amount"`
	Balance       int64       `json:"balance"`
}

type Service struct{ db *postgres.Repository }

func NewService(db *postgres.Repository) *Service { return &Service{db: db} }

func (s *Service) OpenWallet(ctx context.Context, id uuid.UUID, playerID string, opening money.Money, now time.Time) error {
	return s.db.WithTx(ctx, func(ctx context.Context, tx *postgres.Repository) error {
		if _, err := wallet.New(id.String(), playerID, opening); err != nil {
			return err
		}
		if err := postgres.NewWalletRepository(tx).Insert(ctx, postgres.WalletRecord{ID: id, PlayerID: playerID, Currency: opening.Currency(), Balance: opening.Minor(), Version: 1, CreatedAt: now, UpdatedAt: now}); err != nil {
			return err
		}
		if opening.Minor() == 0 {
			return nil
		}
		openingID := uuid.New()
		openingTx, err := wager.NewOpening(openingID.String(), id.String(), opening)
		if err != nil {
			return err
		}
		if err := openingTx.Transition(wager.Processed); err != nil {
			return err
		}
		externalID := "opening-" + id.String()
		if err := postgres.NewWagerTransactionRepository(tx).Insert(ctx, postgres.WagerTransactionRecord{
			ID: openingID, ExternalID: externalID, ProviderID: "internal", WalletID: id,
			PlayerID: playerID, GameID: "internal", RoundID: "opening", Type: string(wager.Opening), Amount: opening.Minor(),
			Currency: opening.Currency(), State: string(wager.Processed), IdempotencyKey: externalID,
			PayloadHash: externalID, CreatedAt: now, UpdatedAt: now,
		}); err != nil {
			return err
		}
		before, _ := money.Zero(opening.Currency())
		entry, err := ledger.New(uuid.New().String(), id.String(), openingID.String(), ledger.Credit, opening, before, opening, now)
		if err != nil {
			return err
		}
		if err := postgres.NewLedgerRepository(tx).Insert(ctx, ledgerRecord(entry)); err != nil {
			return err
		}
		result := Result{TransactionID: openingID, State: wager.Processed, Balance: opening.Minor(), Amount: opening}
		return insertFinancialEvents(ctx, tx, wager.Opening, id, result, 1, now, externalID, true)
	})
}

func (s *Service) Process(ctx context.Context, cmd Command, now time.Time) (Result, error) {
	var result Result
	if cmd.PlayerID == legacyIdentity || cmd.GameID == legacyIdentity || cmd.RoundID == legacyIdentity {
		return result, ErrInvalidCommand
	}
	err := s.db.WithTx(ctx, func(ctx context.Context, tx *postgres.Repository) error {
		wr := postgres.NewWalletRepository(tx)
		w, err := wr.FindForUpdate(ctx, cmd.WalletID)
		if err != nil {
			return err
		}
		txDomain, err := wager.New(cmd.ID.String(), cmd.ExternalID, cmd.ProviderID, cmd.WalletID.String(), cmd.Type, cmd.Amount)
		if err != nil {
			return err
		}
		if cmd.PlayerID == "" || cmd.GameID == "" || cmd.RoundID == "" {
			return ErrInvalidCommand
		}
		if cmd.PlayerID != w.PlayerID {
			return persistRejected(ctx, tx, cmd, w, now, txDomain, &result)
		}
		if cmd.Type == wager.Refund || cmd.Type == wager.Rollback {
			if cmd.ReferenceExternalID == "" {
				return persistPendingReference(ctx, tx, cmd, w, now, txDomain, &result)
			}
			reference, findErr := postgres.NewWagerTransactionRepository(tx).FindByExternal(ctx, cmd.ProviderID, cmd.ReferenceExternalID)
			if errors.Is(findErr, pgx.ErrNoRows) {
				return persistPendingReference(ctx, tx, cmd, w, now, txDomain, &result)
			}
			if findErr != nil {
				return findErr
			}
			if !validReference(cmd, reference) {
				return persistRejected(ctx, tx, cmd, w, now, txDomain, &result)
			}
		}

		before := w.Balance
		after := before
		movement := cmd.Amount.Minor()
		direction := ledger.Debit
		domainWallet, err := walletFromRecord(w)
		if err != nil {
			return err
		}
		var operationErr error
		switch cmd.Type {
		case wager.Bet:
			direction, operationErr = ledger.Debit, domainWallet.Debit(cmd.Amount)
		case wager.Win, wager.Refund:
			direction, operationErr = ledger.Credit, domainWallet.Credit(cmd.Amount)
		case wager.Rollback:
			reference, findErr := postgres.NewWagerTransactionRepository(tx).FindByExternal(ctx, cmd.ProviderID, cmd.ReferenceExternalID)
			if findErr != nil {
				return findErr
			}
			if reference.Type == string(wager.Bet) {
				direction, operationErr = ledger.Credit, domainWallet.Credit(cmd.Amount)
			} else {
				direction, operationErr = ledger.Debit, domainWallet.Debit(cmd.Amount)
			}
		case wager.Loss:
			movement = 0
		default:
			return ErrReferenceInvalid
		}
		state := wager.Processed
		if operationErr != nil {
			state = wager.Rejected
		} else if movement > 0 {
			after = domainWallet.Balance().Minor()
		}
		if err := txDomain.Transition(state); err != nil {
			return err
		}
		result = Result{TransactionID: cmd.ID, State: state, Balance: after, Amount: cmd.Amount}
		if err := insertTransaction(ctx, tx, cmd, state, now); err != nil {
			return err
		}
		if state == wager.Processed && movement > 0 {
			if err := wr.UpdateBalance(ctx, cmd.WalletID, after, w.Version+1, now); err != nil {
				return err
			}
			value, err := moneyFromMinor(movement, cmd.Amount.Currency())
			if err != nil {
				return err
			}
			beforeMoney, err := moneyFromMinor(before, cmd.Amount.Currency())
			if err != nil {
				return err
			}
			afterMoney, err := moneyFromMinor(after, cmd.Amount.Currency())
			if err != nil {
				return err
			}
			entry, err := ledger.New(uuid.New().String(), cmd.WalletID.String(), cmd.ID.String(), direction, value, beforeMoney, afterMoney, now)
			if err != nil {
				return err
			}
			if err := postgres.NewLedgerRepository(tx).Insert(ctx, ledgerRecord(entry)); err != nil {
				return err
			}
		}
		version := int64(w.Version)
		if state == wager.Processed && movement > 0 {
			version++
		}
		if state == wager.Processed {
			return insertFinancialEvents(ctx, tx, cmd.Type, cmd.WalletID, result, version, now, cmd.IdempotencyKey, movement > 0)
		}
		return insertEvent(ctx, tx, "WagerTransactionRejected", cmd.Type, cmd.ID, cmd.WalletID, result, version, now, cmd.IdempotencyKey)
	})
	return result, err
}

func (s *Service) Reconcile(ctx context.Context, walletID uuid.UUID) (Reconciliation, error) {
	w, err := postgres.NewWalletRepository(s.db).Find(ctx, walletID)
	if err != nil {
		return Reconciliation{}, err
	}
	ledgerBalance, err := postgres.NewLedgerRepository(s.db).Reconstruct(ctx, walletID)
	if err != nil {
		return Reconciliation{}, err
	}
	return Reconciliation{WalletBalance: w.Balance, LedgerBalance: ledgerBalance, Consistent: w.Balance == ledgerBalance}, nil
}

func persistPendingReference(ctx context.Context, tx *postgres.Repository, cmd Command, w postgres.WalletRecord, now time.Time, txDomain wager.Transaction, output *Result) error {
	if err := txDomain.Transition(wager.PendingReference); err != nil {
		return err
	}
	if err := insertTransaction(ctx, tx, cmd, wager.PendingReference, now); err != nil {
		return err
	}
	result := Result{TransactionID: cmd.ID, State: wager.PendingReference, Balance: w.Balance, Amount: cmd.Amount}
	*output = result
	return insertEvent(ctx, tx, "WagerTransactionPendingReference", cmd.Type, cmd.ID, cmd.WalletID, result, int64(w.Version), now, cmd.IdempotencyKey)
}

func persistRejected(ctx context.Context, tx *postgres.Repository, cmd Command, w postgres.WalletRecord, now time.Time, txDomain wager.Transaction, output *Result) error {
	if err := txDomain.Transition(wager.Rejected); err != nil {
		return err
	}
	if err := insertTransaction(ctx, tx, cmd, wager.Rejected, now); err != nil {
		return err
	}
	result := Result{TransactionID: cmd.ID, State: wager.Rejected, Balance: w.Balance, Amount: cmd.Amount}
	*output = result
	return insertEvent(ctx, tx, "WagerTransactionRejected", cmd.Type, cmd.ID, cmd.WalletID, result, int64(w.Version), now, cmd.IdempotencyKey)
}

func insertTransaction(ctx context.Context, tx *postgres.Repository, cmd Command, state wager.State, now time.Time) error {
	return postgres.NewWagerTransactionRepository(tx).Insert(ctx, postgres.WagerTransactionRecord{
		ID: cmd.ID, ExternalID: cmd.ExternalID, ProviderID: cmd.ProviderID, WalletID: cmd.WalletID,
		PlayerID: cmd.PlayerID, GameID: cmd.GameID, RoundID: cmd.RoundID, Type: string(cmd.Type), Amount: cmd.Amount.Minor(),
		Currency: cmd.Amount.Currency(), State: string(state), IdempotencyKey: cmd.IdempotencyKey,
		PayloadHash: cmd.PayloadHash, ReferenceExternalID: cmd.ReferenceExternalID, CreatedAt: now, UpdatedAt: now,
	})
}

func insertFinancialEvents(ctx context.Context, tx *postgres.Repository, typ wager.Type, walletID uuid.UUID, result Result, version int64, now time.Time, correlation string, balanceChanged bool) error {
	if err := insertEvent(ctx, tx, "WagerTransactionProcessed", typ, result.TransactionID, walletID, result, version, now, correlation); err != nil {
		return err
	}
	if !balanceChanged {
		return nil
	}
	return insertEvent(ctx, tx, "WalletBalanceChanged", typ, walletID, walletID, result, version, now, correlation)
}

func insertEvent(ctx context.Context, tx *postgres.Repository, eventType string, typ wager.Type, aggregateID, walletID uuid.UUID, result Result, version int64, now time.Time, correlation string) error {
	data, err := json.Marshal(eventPayload{TransactionID: result.TransactionID, WalletID: walletID, Type: typ, State: result.State, Amount: result.Amount, Balance: result.Balance})
	if err != nil {
		return err
	}
	return postgres.NewOutboxRepository(tx).InsertWithMetadata(ctx, uuid.New(), eventType, aggregateID, correlation, result.TransactionID.String(), version, now, data, now)
}

func validReference(cmd Command, reference postgres.WagerTransactionRecord) bool {
	if reference.State != string(wager.Processed) || reference.WalletID != cmd.WalletID || reference.PlayerID != cmd.PlayerID || reference.GameID != cmd.GameID || reference.RoundID != cmd.RoundID || reference.Currency != cmd.Amount.Currency() || reference.Amount != cmd.Amount.Minor() {
		return false
	}
	if cmd.Type == wager.Refund {
		return reference.Type == string(wager.Bet)
	}
	return reference.Type == string(wager.Bet) || reference.Type == string(wager.Win) || reference.Type == string(wager.Refund)
}

func ledgerRecord(entry ledger.Entry) postgres.LedgerRecord {
	return postgres.LedgerRecord{ID: uuid.MustParse(entry.ID()), WalletID: uuid.MustParse(entry.WalletID()), TransactionID: uuid.MustParse(entry.TransactionID()), Direction: string(entry.Direction()), Value: entry.Value().Minor(), Currency: entry.Value().Currency(), BalanceBefore: entry.BalanceBefore().Minor(), BalanceAfter: entry.BalanceAfter().Minor(), Timestamp: entry.Timestamp()}
}

func moneyFromMinor(minor int64, currency string) (money.Money, error) {
	return money.New(fmt.Sprintf("%d.%02d", minor/100, minor%100), currency)
}

func walletFromRecord(w postgres.WalletRecord) (wallet.Wallet, error) {
	m, err := moneyFromMinor(w.Balance, w.Currency)
	if err != nil {
		return wallet.Wallet{}, err
	}
	return wallet.New(w.ID.String(), w.PlayerID, m)
}
