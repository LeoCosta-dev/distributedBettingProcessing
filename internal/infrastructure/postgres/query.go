package postgres

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// LedgerCursor is the keyset position used to resume a ledger listing. It is a
// value object of the read path: it only carries the two persisted columns that
// make the ledger ordering total and stable.
type LedgerCursor struct {
	Timestamp time.Time
	ID        uuid.UUID
}

// LedgerQueryRepository exposes read-only ledger access.
//
// It is a separate, additive type: the transactional repositories keep their
// existing semantics untouched.
type LedgerQueryRepository struct{ db *Repository }

func NewLedgerQueryRepository(db *Repository) *LedgerQueryRepository {
	return &LedgerQueryRepository{db: db}
}

// ListByWallet returns at most limit entries ordered by (timestamp, id).
//
// The caller is expected to request limit+1 rows: the extra row only signals
// that another page exists and is never exposed. Ordering matches the
// reconstruction order used by LedgerRepository.Reconstruct, and it is total
// because id is the primary key.
func (r *LedgerQueryRepository) ListByWallet(ctx context.Context, walletID uuid.UUID, after *LedgerCursor, limit int) ([]LedgerRecord, error) {
	const columns = `id,wallet_id,transaction_id,direction,value,currency,balance_before,balance_after,timestamp`
	query := `SELECT ` + columns + ` FROM wallet_ledger_entries WHERE wallet_id=$1 ORDER BY timestamp, id LIMIT $2`
	args := []any{walletID, limit}
	if after != nil {
		query = `SELECT ` + columns + ` FROM wallet_ledger_entries WHERE wallet_id=$1 AND (timestamp, id) > ($2,$3) ORDER BY timestamp, id LIMIT $4`
		args = []any{walletID, after.Timestamp, after.ID, limit}
	}
	rows, err := r.db.exec.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	entries := make([]LedgerRecord, 0, limit)
	for rows.Next() {
		var entry LedgerRecord
		if err := rows.Scan(&entry.ID, &entry.WalletID, &entry.TransactionID, &entry.Direction, &entry.Value, &entry.Currency, &entry.BalanceBefore, &entry.BalanceAfter, &entry.Timestamp); err != nil {
			return nil, err
		}
		entries = append(entries, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return entries, nil
}

// CountByWallet returns the number of persisted ledger entries for a wallet.
// It is used by read-only reconciliation reporting and does not load entries.
func (r *LedgerQueryRepository) CountByWallet(ctx context.Context, walletID uuid.UUID) (int64, error) {
	var count int64
	if err := r.db.exec.QueryRow(ctx, `SELECT count(*) FROM wallet_ledger_entries WHERE wallet_id=$1`, walletID).Scan(&count); err != nil {
		return 0, err
	}
	return count, nil
}

// WagerTransactionQueryRepository exposes provider-filtered transaction reads.
//
// The provider filter is applied by PostgreSQL itself, so provider isolation
// happens before any transaction data is exposed to the caller.
type WagerTransactionQueryRepository struct{ db *Repository }

func NewWagerTransactionQueryRepository(db *Repository) *WagerTransactionQueryRepository {
	return &WagerTransactionQueryRepository{db: db}
}

func (r *WagerTransactionQueryRepository) FindByIDForProvider(ctx context.Context, providerID string, id uuid.UUID) (WagerTransactionRecord, error) {
	return r.find(ctx, `SELECT `+wagerTransactionColumns+` FROM wager_transactions WHERE id=$1 AND provider_id=$2`, id, providerID)
}

func (r *WagerTransactionQueryRepository) FindByExternalForProvider(ctx context.Context, providerID, externalID string) (WagerTransactionRecord, error) {
	return r.find(ctx, `SELECT `+wagerTransactionColumns+` FROM wager_transactions WHERE provider_id=$1 AND external_id=$2`, providerID, externalID)
}

func (r *WagerTransactionQueryRepository) find(ctx context.Context, query string, args ...any) (WagerTransactionRecord, error) {
	var record WagerTransactionRecord
	var reference *string
	err := r.db.exec.QueryRow(ctx, query, args...).Scan(&record.ID, &record.ExternalID, &record.ProviderID, &record.WalletID, &record.PlayerID, &record.GameID, &record.RoundID, &record.Type, &record.Amount, &record.Currency, &record.State, &record.IdempotencyKey, &record.PayloadHash, &record.Result, &reference, &record.CreatedAt, &record.UpdatedAt)
	if err != nil {
		return WagerTransactionRecord{}, err
	}
	if reference != nil {
		record.ReferenceExternalID = *reference
	}
	return record, nil
}
