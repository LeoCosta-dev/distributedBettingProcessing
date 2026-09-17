package postgres

import (
	"context"
	"errors"
	"github.com/google/uuid"
	"sort"
	"time"
)

var ErrLedgerInconsistent = errors.New("ledger is inconsistent")

type WalletRecord struct {
	ID                   uuid.UUID
	PlayerID, Currency   string
	Balance              int64
	Version              int64
	CreatedAt, UpdatedAt time.Time
}
type WagerTransactionRecord struct {
	ID                                                                uuid.UUID
	ExternalID, ProviderID                                            string
	WalletID                                                          uuid.UUID
	PlayerID, GameID, RoundID                                         string
	Type                                                              string
	Amount                                                            int64
	Currency, State, IdempotencyKey, PayloadHash, ReferenceExternalID string
	Result                                                            []byte
	CreatedAt, UpdatedAt                                              time.Time
}
type LedgerRecord struct {
	ID, WalletID, TransactionID        uuid.UUID
	Direction, Currency                string
	Value, BalanceBefore, BalanceAfter int64
	Timestamp                          time.Time
}
type InboxRecord struct {
	ConsumerName, MessageID, PayloadHash string
	ReceivedAt                           time.Time
	CompletedAt                          *time.Time
}

type WalletRepository struct{ db *Repository }

func NewWalletRepository(db *Repository) *WalletRepository { return &WalletRepository{db: db} }
func (r *WalletRepository) Insert(ctx context.Context, w WalletRecord) error {
	_, err := r.db.exec.Exec(ctx, `INSERT INTO wallets (id,player_id,currency,balance,version,created_at,updated_at) VALUES ($1,$2,$3,$4,$5,$6,$7)`, w.ID, w.PlayerID, w.Currency, w.Balance, w.Version, w.CreatedAt, w.UpdatedAt)
	return err
}
func (r *WalletRepository) Find(ctx context.Context, id uuid.UUID) (WalletRecord, error) {
	var w WalletRecord
	err := r.db.exec.QueryRow(ctx, `SELECT id,player_id,currency,balance,version,created_at,updated_at FROM wallets WHERE id=$1`, id).Scan(&w.ID, &w.PlayerID, &w.Currency, &w.Balance, &w.Version, &w.CreatedAt, &w.UpdatedAt)
	return w, err
}
func (r *WalletRepository) FindForUpdate(ctx context.Context, id uuid.UUID) (WalletRecord, error) {
	var w WalletRecord
	err := r.db.exec.QueryRow(ctx, `SELECT id,player_id,currency,balance,version,created_at,updated_at FROM wallets WHERE id=$1 FOR UPDATE`, id).Scan(&w.ID, &w.PlayerID, &w.Currency, &w.Balance, &w.Version, &w.CreatedAt, &w.UpdatedAt)
	return w, err
}
func (r *WalletRepository) UpdateBalance(ctx context.Context, id uuid.UUID, balance, version int64, updatedAt time.Time) error {
	_, err := r.db.exec.Exec(ctx, `UPDATE wallets SET balance=$2, version=$3, updated_at=$4 WHERE id=$1`, id, balance, version, updatedAt)
	return err
}

type WagerTransactionRepository struct{ db *Repository }

func NewWagerTransactionRepository(db *Repository) *WagerTransactionRepository {
	return &WagerTransactionRepository{db: db}
}
func (r *WagerTransactionRepository) Insert(ctx context.Context, w WagerTransactionRecord) error {
	var reference any = w.ReferenceExternalID
	if w.ReferenceExternalID == "" {
		reference = nil
	}
	_, err := r.db.exec.Exec(ctx, `INSERT INTO wager_transactions (id,external_id,provider_id,wallet_id,player_id,game_id,round_id,type,amount,currency,state,idempotency_key,payload_hash,result,reference_external_id,created_at,updated_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17)`, w.ID, w.ExternalID, w.ProviderID, w.WalletID, w.PlayerID, w.GameID, w.RoundID, w.Type, w.Amount, w.Currency, w.State, w.IdempotencyKey, w.PayloadHash, w.Result, reference, w.CreatedAt, w.UpdatedAt)
	return err
}
func (r *WagerTransactionRepository) FindByExternal(ctx context.Context, provider, external string) (WagerTransactionRecord, error) {
	var w WagerTransactionRecord
	var reference *string
	err := r.db.exec.QueryRow(ctx, `SELECT id,external_id,provider_id,wallet_id,player_id,game_id,round_id,type,amount,currency,state,idempotency_key,payload_hash,result,reference_external_id,created_at,updated_at FROM wager_transactions WHERE provider_id=$1 AND external_id=$2`, provider, external).Scan(&w.ID, &w.ExternalID, &w.ProviderID, &w.WalletID, &w.PlayerID, &w.GameID, &w.RoundID, &w.Type, &w.Amount, &w.Currency, &w.State, &w.IdempotencyKey, &w.PayloadHash, &w.Result, &reference, &w.CreatedAt, &w.UpdatedAt)
	if reference != nil {
		w.ReferenceExternalID = *reference
	}
	return w, err
}

type LedgerRepository struct{ db *Repository }

func NewLedgerRepository(db *Repository) *LedgerRepository { return &LedgerRepository{db: db} }
func (r *LedgerRepository) Insert(ctx context.Context, e LedgerRecord) error {
	_, err := r.db.exec.Exec(ctx, `INSERT INTO wallet_ledger_entries (id,wallet_id,transaction_id,direction,value,currency,balance_before,balance_after,timestamp) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`, e.ID, e.WalletID, e.TransactionID, e.Direction, e.Value, e.Currency, e.BalanceBefore, e.BalanceAfter, e.Timestamp)
	return err
}
func (r *LedgerRepository) Count(ctx context.Context, walletID uuid.UUID) (int64, error) {
	var n int64
	err := r.db.exec.QueryRow(ctx, `SELECT count(*) FROM wallet_ledger_entries WHERE wallet_id=$1`, walletID).Scan(&n)
	return n, err
}
func (r *LedgerRepository) Reconstruct(ctx context.Context, walletID uuid.UUID) (int64, error) {
	var currency string
	if err := r.db.exec.QueryRow(ctx, `SELECT currency FROM wallets WHERE id=$1`, walletID).Scan(&currency); err != nil {
		return 0, err
	}
	rows, err := r.db.exec.Query(ctx, `SELECT direction,value,currency,balance_before,balance_after,timestamp,id FROM wallet_ledger_entries WHERE wallet_id=$1 ORDER BY timestamp,id`, walletID)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	type entry struct {
		direction, currency  string
		value, before, after int64
		timestamp            time.Time
		id                   uuid.UUID
	}
	var entries []entry
	for rows.Next() {
		var current entry
		if err := rows.Scan(&current.direction, &current.value, &current.currency, &current.before, &current.after, &current.timestamp, &current.id); err != nil {
			return 0, err
		}
		if current.value <= 0 || current.before < 0 || current.after < 0 || current.currency != currency {
			return 0, ErrLedgerInconsistent
		}
		switch current.direction {
		case "CREDIT":
			if current.after < current.before || current.after-current.before != current.value {
				return 0, ErrLedgerInconsistent
			}
		case "DEBIT":
			if current.before < current.after || current.before-current.after != current.value {
				return 0, ErrLedgerInconsistent
			}
		default:
			return 0, ErrLedgerInconsistent
		}
		entries = append(entries, current)
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	// The persisted timestamp/id order makes candidate selection reproducible;
	// balance continuity determines the actual financial sequence.
	sort.SliceStable(entries, func(i, j int) bool {
		if entries[i].timestamp.Equal(entries[j].timestamp) {
			return entries[i].id.String() < entries[j].id.String()
		}
		return entries[i].timestamp.Before(entries[j].timestamp)
	})
	var balance int64
	for len(entries) > 0 {
		candidate := -1
		for i := range entries {
			if entries[i].before == balance {
				if candidate >= 0 {
					return 0, ErrLedgerInconsistent
				}
				candidate = i
			}
		}
		if candidate < 0 {
			return 0, ErrLedgerInconsistent
		}
		balance = entries[candidate].after
		entries = append(entries[:candidate], entries[candidate+1:]...)
	}
	return balance, nil
}

type InboxRepository struct{ db *Repository }

func NewInboxRepository(db *Repository) *InboxRepository { return &InboxRepository{db: db} }
func (r *InboxRepository) Insert(ctx context.Context, i InboxRecord) error {
	_, err := r.db.exec.Exec(ctx, `INSERT INTO inbox (consumer_name,message_id,payload_hash,received_at,completed_at) VALUES ($1,$2,$3,$4,$5)`, i.ConsumerName, i.MessageID, i.PayloadHash, i.ReceivedAt, i.CompletedAt)
	return err
}

type OutboxRepository struct{ db *Repository }

func NewOutboxRepository(db *Repository) *OutboxRepository { return &OutboxRepository{db: db} }
func (r *OutboxRepository) Insert(ctx context.Context, eventID uuid.UUID, eventType string, aggregateID uuid.UUID, occurredAt time.Time, data []byte, nextAttemptAt time.Time) error {
	return r.InsertWithMetadata(ctx, eventID, eventType, aggregateID, "", "", 1, occurredAt, data, nextAttemptAt)
}
func (r *OutboxRepository) InsertWithMetadata(ctx context.Context, eventID uuid.UUID, eventType string, aggregateID uuid.UUID, correlationID, causationID string, version int64, occurredAt time.Time, data []byte, nextAttemptAt time.Time) error {
	_, err := r.db.exec.Exec(ctx, `INSERT INTO outbox (event_id,event_type,aggregate_id,correlation_id,causation_id,occurred_at,version,data,status,next_attempt_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,'PENDING',$9)`, eventID, eventType, aggregateID, nullableString(correlationID), nullableString(causationID), occurredAt, version, data, nextAttemptAt)
	return err
}
func (r *OutboxRepository) CountByTypeAndAggregate(ctx context.Context, eventType string, aggregateID uuid.UUID) (int64, error) {
	var count int64
	err := r.db.exec.QueryRow(ctx, `SELECT count(*) FROM outbox WHERE event_type=$1 AND aggregate_id=$2`, eventType, aggregateID).Scan(&count)
	return count, err
}
func nullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
}
func (r *OutboxRepository) ClaimOne(ctx context.Context, now time.Time) (uuid.UUID, error) {
	var id uuid.UUID
	err := r.db.exec.QueryRow(ctx, `UPDATE outbox SET status='CLAIMED', claimed_at=$1, attempts=attempts+1 WHERE event_id=(SELECT event_id FROM outbox WHERE status='PENDING' AND next_attempt_at <= $1 ORDER BY next_attempt_at FOR UPDATE SKIP LOCKED LIMIT 1) RETURNING event_id`, now).Scan(&id)
	return id, err
}
