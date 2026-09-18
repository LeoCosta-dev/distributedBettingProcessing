// Package query implements the read-only application boundary consumed by the
// HTTP adapter. It contains no financial rules and never mutates state: every
// method reads persisted data that the financial use cases already committed.
package query

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/leonardodacosta/distributedBettingProcessing/internal/domain/money"
	"github.com/leonardodacosta/distributedBettingProcessing/internal/domain/wager"
	"github.com/leonardodacosta/distributedBettingProcessing/internal/infrastructure/postgres"
)

const (
	// DefaultLedgerLimit is applied when the caller does not ask for a page size.
	DefaultLedgerLimit = 50
	// MaxLedgerLimit bounds the page size so a single request can never load the
	// whole ledger into memory.
	MaxLedgerLimit = 100
)

var (
	// ErrWalletNotFound classifies an unknown wallet.
	ErrWalletNotFound = errors.New("wallet not found")
	// ErrTransactionNotFound classifies an unknown or non-owned transaction.
	ErrTransactionNotFound = errors.New("transaction not found")
	// ErrInvalidCursor classifies a malformed or undecodable pagination cursor.
	ErrInvalidCursor = errors.New("invalid ledger cursor")
	// ErrInvalidLimit classifies an out-of-range page size.
	ErrInvalidLimit = errors.New("invalid ledger limit")
)

// WalletView is the persisted wallet state.
type WalletView struct {
	ID           uuid.UUID
	PlayerID     string
	Currency     string
	BalanceMinor int64
	Version      int64
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// LedgerEntryView is a persisted, immutable ledger entry.
type LedgerEntryView struct {
	ID                 uuid.UUID
	TransactionID      uuid.UUID
	Direction          string
	ValueMinor         int64
	Currency           string
	BalanceBeforeMinor int64
	BalanceAfterMinor  int64
	Timestamp          time.Time
}

// LedgerPage is one deterministic slice of the wallet ledger.
type LedgerPage struct {
	WalletID   uuid.UUID
	Currency   string
	Entries    []LedgerEntryView
	NextCursor string
}

// TransactionView is the persisted wagering transaction together with the
// processing snapshot that was committed with it.
type TransactionView struct {
	ID                  uuid.UUID
	ExternalID          string
	ProviderID          string
	WalletID            uuid.UUID
	PlayerID            string
	GameID              string
	RoundID             string
	Type                wager.Type
	State               wager.State
	AmountMinor         int64
	Currency            string
	ReferenceExternalID string
	CreatedAt           time.Time
	UpdatedAt           time.Time
	// BalanceMinor is the balance snapshot persisted with the transaction. It is
	// nil when the record has no compatible snapshot; it is never derived from
	// the wallet's current balance.
	BalanceMinor *int64
}

// Service is the read-only application boundary for wallets, ledgers and
// provider-scoped transaction lookups.
type Service struct {
	db           *postgres.Repository
	wallets      *postgres.WalletRepository
	ledger       *postgres.LedgerQueryRepository
	transactions *postgres.WagerTransactionQueryRepository
}

func NewService(db *postgres.Repository) *Service {
	return &Service{
		db:           db,
		wallets:      postgres.NewWalletRepository(db),
		ledger:       postgres.NewLedgerQueryRepository(db),
		transactions: postgres.NewWagerTransactionQueryRepository(db),
	}
}

// Wallet returns the persisted wallet or ErrWalletNotFound.
func (s *Service) Wallet(ctx context.Context, walletID uuid.UUID) (WalletView, error) {
	record, err := s.wallets.Find(ctx, walletID)
	if errors.Is(err, pgx.ErrNoRows) {
		return WalletView{}, ErrWalletNotFound
	}
	if err != nil {
		return WalletView{}, err
	}
	return WalletView{
		ID:           record.ID,
		PlayerID:     record.PlayerID,
		Currency:     record.Currency,
		BalanceMinor: record.Balance,
		Version:      record.Version,
		CreatedAt:    record.CreatedAt,
		UpdatedAt:    record.UpdatedAt,
	}, nil
}

// Ledger returns at most one page of the wallet ledger, ordered by
// (timestamp, id). The order is total because id is the primary key, and it is
// stable because ledger entries are append-only and immutable.
//
// One extra row is fetched to decide whether a next page exists; it is never
// returned to the caller.
func (s *Service) Ledger(ctx context.Context, walletID uuid.UUID, rawCursor string, rawLimit string) (LedgerPage, error) {
	limit, err := parseLimit(rawLimit)
	if err != nil {
		return LedgerPage{}, err
	}
	var after *postgres.LedgerCursor
	if rawCursor != "" {
		decoded, err := decodeCursor(rawCursor)
		if err != nil {
			return LedgerPage{}, err
		}
		after = &decoded
	}
	wallet, err := s.Wallet(ctx, walletID)
	if err != nil {
		return LedgerPage{}, err
	}
	records, err := s.ledger.ListByWallet(ctx, walletID, after, limit+1)
	if err != nil {
		return LedgerPage{}, err
	}
	page := LedgerPage{WalletID: walletID, Currency: wallet.Currency, Entries: make([]LedgerEntryView, 0, limit)}
	if len(records) > limit {
		records = records[:limit]
		last := records[len(records)-1]
		page.NextCursor = encodeCursor(last.Timestamp, last.ID)
	}
	for _, record := range records {
		page.Entries = append(page.Entries, LedgerEntryView{
			ID:                 record.ID,
			TransactionID:      record.TransactionID,
			Direction:          record.Direction,
			ValueMinor:         record.Value,
			Currency:           record.Currency,
			BalanceBeforeMinor: record.BalanceBefore,
			BalanceAfterMinor:  record.BalanceAfter,
			Timestamp:          record.Timestamp,
		})
	}
	return page, nil
}

// LedgerEntryCount returns the persisted entry count without loading the
// ledger. It is a read-only value used by reconciliation reporting.
func (s *Service) LedgerEntryCount(ctx context.Context, walletID uuid.UUID) (int64, error) {
	return s.ledger.CountByWallet(ctx, walletID)
}

// TransactionForProvider reads a transaction only when it belongs to the
// authenticated provider. The provider filter is applied by PostgreSQL, so no
// other provider's data is exposed.
func (s *Service) TransactionForProvider(ctx context.Context, providerID string, transactionID uuid.UUID) (TransactionView, error) {
	record, err := s.transactions.FindByIDForProvider(ctx, providerID, transactionID)
	if errors.Is(err, pgx.ErrNoRows) {
		return TransactionView{}, ErrTransactionNotFound
	}
	if err != nil {
		return TransactionView{}, err
	}
	return transactionView(record), nil
}

// TransactionByExternalForProvider reads a transaction by its external identity
// inside the authenticated provider scope.
func (s *Service) TransactionByExternalForProvider(ctx context.Context, providerID, externalID string) (TransactionView, error) {
	record, err := s.transactions.FindByExternalForProvider(ctx, providerID, externalID)
	if errors.Is(err, pgx.ErrNoRows) {
		return TransactionView{}, ErrTransactionNotFound
	}
	if err != nil {
		return TransactionView{}, err
	}
	return transactionView(record), nil
}

func transactionView(record postgres.WagerTransactionRecord) TransactionView {
	view := TransactionView{
		ID:                  record.ID,
		ExternalID:          record.ExternalID,
		ProviderID:          record.ProviderID,
		WalletID:            record.WalletID,
		PlayerID:            record.PlayerID,
		GameID:              record.GameID,
		RoundID:             record.RoundID,
		Type:                wager.Type(record.Type),
		State:               wager.State(record.State),
		AmountMinor:         record.Amount,
		Currency:            record.Currency,
		ReferenceExternalID: record.ReferenceExternalID,
		CreatedAt:           record.CreatedAt,
		UpdatedAt:           record.UpdatedAt,
	}
	if balance, ok := persistedBalance(record); ok {
		view.BalanceMinor = &balance
	}
	return view
}

// persistedBalance extracts the balance snapshot committed with the transaction.
// The snapshot is trusted only when it is consistent with the persisted record,
// mirroring the validation the financial service performs before replaying it.
// A missing or incompatible snapshot yields no balance instead of a value
// reconstructed from the wallet's current state.
func persistedBalance(record postgres.WagerTransactionRecord) (int64, bool) {
	if len(record.Result) == 0 {
		return 0, false
	}
	var snapshot struct {
		TransactionID uuid.UUID   `json:"transactionId"`
		State         wager.State `json:"state"`
		Balance       int64       `json:"balance"`
		Amount        money.Money `json:"amount"`
	}
	if err := json.Unmarshal(record.Result, &snapshot); err != nil {
		return 0, false
	}
	if snapshot.TransactionID != record.ID || string(snapshot.State) != record.State || snapshot.Balance < 0 {
		return 0, false
	}
	if snapshot.Amount.Minor() != record.Amount || snapshot.Amount.Currency() != record.Currency {
		return 0, false
	}
	return snapshot.Balance, true
}

func parseLimit(raw string) (int, error) {
	if raw == "" {
		return DefaultLedgerLimit, nil
	}
	parsed, err := strconv.Atoi(raw)
	if err != nil || parsed < 1 || parsed > MaxLedgerLimit {
		return 0, fmt.Errorf("%w: %q must be between 1 and %d", ErrInvalidLimit, raw, MaxLedgerLimit)
	}
	return parsed, nil
}

type cursorPayload struct {
	Timestamp string    `json:"ts"`
	ID        uuid.UUID `json:"id"`
}

// encodeCursor produces an opaque token. The payload exposes only the two
// persisted columns that define the total order.
func encodeCursor(timestamp time.Time, id uuid.UUID) string {
	payload, err := json.Marshal(cursorPayload{Timestamp: timestamp.UTC().Format(time.RFC3339Nano), ID: id})
	if err != nil {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString(payload)
}

func decodeCursor(raw string) (postgres.LedgerCursor, error) {
	decoded, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return postgres.LedgerCursor{}, fmt.Errorf("%w: not base64url", ErrInvalidCursor)
	}
	var payload cursorPayload
	if err := json.Unmarshal(decoded, &payload); err != nil {
		return postgres.LedgerCursor{}, fmt.Errorf("%w: not a cursor payload", ErrInvalidCursor)
	}
	timestamp, err := time.Parse(time.RFC3339Nano, payload.Timestamp)
	if err != nil || payload.ID == uuid.Nil {
		return postgres.LedgerCursor{}, fmt.Errorf("%w: malformed cursor position", ErrInvalidCursor)
	}
	return postgres.LedgerCursor{Timestamp: timestamp, ID: payload.ID}, nil
}
