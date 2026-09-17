package postgres

import (
	"context"
	"github.com/google/uuid"
	"time"
)

type WalletRecord struct {
	ID                   uuid.UUID
	PlayerID, Currency   string
	Balance              int64
	Version              int64
	CreatedAt, UpdatedAt time.Time
}
type WagerTransactionRecord struct {
	ID                                           uuid.UUID
	ExternalID, ProviderID                       string
	WalletID                                     uuid.UUID
	Type                                         string
	Amount                                       int64
	Currency, State, IdempotencyKey, PayloadHash string
	Result                                       []byte
	CreatedAt, UpdatedAt                         time.Time
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

type WagerTransactionRepository struct{ db *Repository }

func NewWagerTransactionRepository(db *Repository) *WagerTransactionRepository {
	return &WagerTransactionRepository{db: db}
}
func (r *WagerTransactionRepository) Insert(ctx context.Context, w WagerTransactionRecord) error {
	_, err := r.db.exec.Exec(ctx, `INSERT INTO wager_transactions (id,external_id,provider_id,wallet_id,type,amount,currency,state,idempotency_key,payload_hash,result,created_at,updated_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)`, w.ID, w.ExternalID, w.ProviderID, w.WalletID, w.Type, w.Amount, w.Currency, w.State, w.IdempotencyKey, w.PayloadHash, w.Result, w.CreatedAt, w.UpdatedAt)
	return err
}
func (r *WagerTransactionRepository) FindByExternal(ctx context.Context, provider, external string) (WagerTransactionRecord, error) {
	var w WagerTransactionRecord
	err := r.db.exec.QueryRow(ctx, `SELECT id,external_id,provider_id,wallet_id,type,amount,currency,state,idempotency_key,payload_hash,result,created_at,updated_at FROM wager_transactions WHERE provider_id=$1 AND external_id=$2`, provider, external).Scan(&w.ID, &w.ExternalID, &w.ProviderID, &w.WalletID, &w.Type, &w.Amount, &w.Currency, &w.State, &w.IdempotencyKey, &w.PayloadHash, &w.Result, &w.CreatedAt, &w.UpdatedAt)
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

type InboxRepository struct{ db *Repository }

func NewInboxRepository(db *Repository) *InboxRepository { return &InboxRepository{db: db} }
func (r *InboxRepository) Insert(ctx context.Context, i InboxRecord) error {
	_, err := r.db.exec.Exec(ctx, `INSERT INTO inbox (consumer_name,message_id,payload_hash,received_at,completed_at) VALUES ($1,$2,$3,$4,$5)`, i.ConsumerName, i.MessageID, i.PayloadHash, i.ReceivedAt, i.CompletedAt)
	return err
}

type OutboxRepository struct{ db *Repository }

func NewOutboxRepository(db *Repository) *OutboxRepository { return &OutboxRepository{db: db} }
func (r *OutboxRepository) Insert(ctx context.Context, eventID uuid.UUID, eventType string, aggregateID uuid.UUID, occurredAt time.Time, data []byte, nextAttemptAt time.Time) error {
	_, err := r.db.exec.Exec(ctx, `INSERT INTO outbox (event_id,event_type,aggregate_id,occurred_at,version,data,status,next_attempt_at) VALUES ($1,$2,$3,$4,1,$5,'PENDING',$6)`, eventID, eventType, aggregateID, occurredAt, data, nextAttemptAt)
	return err
}
func (r *OutboxRepository) ClaimOne(ctx context.Context, now time.Time) (uuid.UUID, error) {
	var id uuid.UUID
	err := r.db.exec.QueryRow(ctx, `UPDATE outbox SET status='CLAIMED', claimed_at=$1, attempts=attempts+1 WHERE event_id=(SELECT event_id FROM outbox WHERE status='PENDING' AND next_attempt_at <= $1 ORDER BY next_attempt_at FOR UPDATE SKIP LOCKED LIMIT 1) RETURNING event_id`, now).Scan(&id)
	return id, err
}
