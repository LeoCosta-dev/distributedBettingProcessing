package http

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/leonardodacosta/distributedBettingProcessing/internal/application/financial"
	"github.com/leonardodacosta/distributedBettingProcessing/internal/application/query"
	"github.com/leonardodacosta/distributedBettingProcessing/internal/domain/money"
)

// FinancialUseCases is the write side of the application boundary consumed by
// this adapter. It is satisfied by the financial application service and is the
// same use case surface every other transport must call, so no financial rule
// can be expressed or duplicated here.
type FinancialUseCases interface {
	Process(ctx context.Context, command financial.Command, now time.Time) (financial.Result, error)
	OpenWallet(ctx context.Context, walletID uuid.UUID, playerID string, opening money.Money, now time.Time) error
	Reconcile(ctx context.Context, walletID uuid.UUID) (financial.Reconciliation, error)
}

// QueryUseCases is the read side of the application boundary. Reads never
// mutate state and the provider-scoped lookups are filtered by PostgreSQL.
type QueryUseCases interface {
	Wallet(ctx context.Context, walletID uuid.UUID) (query.WalletView, error)
	Ledger(ctx context.Context, walletID uuid.UUID, cursor, limit string) (query.LedgerPage, error)
	LedgerEntryCount(ctx context.Context, walletID uuid.UUID) (int64, error)
	TransactionForProvider(ctx context.Context, providerID string, transactionID uuid.UUID) (query.TransactionView, error)
	TransactionByExternalForProvider(ctx context.Context, providerID, externalID string) (query.TransactionView, error)
}
