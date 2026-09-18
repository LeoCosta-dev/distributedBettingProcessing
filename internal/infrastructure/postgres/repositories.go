package postgres

import (
	"context"
	"errors"
	"github.com/google/uuid"
	"sort"
	"time"
)

var ErrLedgerInconsistent = errors.New("ledger is inconsistent")
var ErrOutboxClaimLost = errors.New("outbox claim is no longer owned")

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
	ReferenceAttempts                                                 int
	ReferenceNextAttemptAt                                            *time.Time
	FailureCode                                                       string
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

const wagerTransactionColumns = `id,external_id,provider_id,wallet_id,player_id,game_id,round_id,type,amount,currency,state,idempotency_key,payload_hash,result,reference_external_id,reference_attempts,reference_next_attempt_at,failure_code,created_at,updated_at`

func NewWagerTransactionRepository(db *Repository) *WagerTransactionRepository {
	return &WagerTransactionRepository{db: db}
}
func (r *WagerTransactionRepository) Insert(ctx context.Context, w WagerTransactionRecord) error {
	var reference any = w.ReferenceExternalID
	if w.ReferenceExternalID == "" {
		reference = nil
	}
	_, err := r.db.exec.Exec(ctx, `INSERT INTO wager_transactions (id,external_id,provider_id,wallet_id,player_id,game_id,round_id,type,amount,currency,state,idempotency_key,payload_hash,result,reference_external_id,reference_attempts,reference_next_attempt_at,failure_code,created_at,updated_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20)`, w.ID, w.ExternalID, w.ProviderID, w.WalletID, w.PlayerID, w.GameID, w.RoundID, w.Type, w.Amount, w.Currency, w.State, w.IdempotencyKey, w.PayloadHash, w.Result, reference, w.ReferenceAttempts, w.ReferenceNextAttemptAt, nullableString(w.FailureCode), w.CreatedAt, w.UpdatedAt)
	return err
}
func (r *WagerTransactionRepository) FindByExternal(ctx context.Context, provider, external string) (WagerTransactionRecord, error) {
	return r.find(ctx, `SELECT `+wagerTransactionColumns+` FROM wager_transactions WHERE provider_id=$1 AND external_id=$2`, provider, external)
}

// LockReferenceIdentity serializes creation/confirmation of a reference with
// a pending-reference worker's terminal decision. The lock is transaction
// scoped and therefore works across independent application instances.
func (r *WagerTransactionRepository) LockReferenceIdentity(ctx context.Context, provider, external string) error {
	_, err := r.db.exec.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, provider+"\x1f"+external)
	return err
}
func (r *WagerTransactionRepository) FindByID(ctx context.Context, id uuid.UUID) (WagerTransactionRecord, error) {
	return r.find(ctx, `SELECT `+wagerTransactionColumns+` FROM wager_transactions WHERE id=$1`, id)
}
func (r *WagerTransactionRepository) FindByIdempotency(ctx context.Context, provider, key string) (WagerTransactionRecord, error) {
	return r.find(ctx, `SELECT `+wagerTransactionColumns+` FROM wager_transactions WHERE provider_id=$1 AND idempotency_key=$2`, provider, key)
}
func (r *WagerTransactionRepository) CountByExternal(ctx context.Context, provider, external string) (int64, error) {
	var count int64
	err := r.db.exec.QueryRow(ctx, `SELECT count(*) FROM wager_transactions WHERE provider_id=$1 AND external_id=$2`, provider, external).Scan(&count)
	return count, err
}
func (r *WagerTransactionRepository) CountByIdempotency(ctx context.Context, provider, key string) (int64, error) {
	var count int64
	err := r.db.exec.QueryRow(ctx, `SELECT count(*) FROM wager_transactions WHERE provider_id=$1 AND idempotency_key=$2`, provider, key).Scan(&count)
	return count, err
}
func (r *WagerTransactionRepository) find(ctx context.Context, query string, args ...any) (WagerTransactionRecord, error) {
	var w WagerTransactionRecord
	var reference, failureCode *string
	err := r.db.exec.QueryRow(ctx, query, args...).Scan(&w.ID, &w.ExternalID, &w.ProviderID, &w.WalletID, &w.PlayerID, &w.GameID, &w.RoundID, &w.Type, &w.Amount, &w.Currency, &w.State, &w.IdempotencyKey, &w.PayloadHash, &w.Result, &reference, &w.ReferenceAttempts, &w.ReferenceNextAttemptAt, &failureCode, &w.CreatedAt, &w.UpdatedAt)
	if reference != nil {
		w.ReferenceExternalID = *reference
	}
	if failureCode != nil {
		w.FailureCode = *failureCode
	}
	return w, err
}

func (r *WagerTransactionRepository) FindNextPendingReferenceForUpdate(ctx context.Context, now time.Time) (WagerTransactionRecord, error) {
	return r.find(ctx, `SELECT `+wagerTransactionColumns+` FROM wager_transactions WHERE state='PENDING_REFERENCE' AND (reference_next_attempt_at IS NULL OR reference_next_attempt_at <= $1) ORDER BY reference_next_attempt_at NULLS FIRST, id FOR UPDATE SKIP LOCKED LIMIT 1`, now)
}

func (r *WagerTransactionRepository) UpdatePendingReference(ctx context.Context, record WagerTransactionRecord) error {
	_, err := r.db.exec.Exec(ctx, `UPDATE wager_transactions SET state=$2,result=$3,reference_attempts=$4,reference_next_attempt_at=$5,failure_code=$6,updated_at=$7 WHERE id=$1`, record.ID, record.State, record.Result, record.ReferenceAttempts, record.ReferenceNextAttemptAt, nullableString(record.FailureCode), record.UpdatedAt)
	return err
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
func (r *LedgerRepository) CountByTransaction(ctx context.Context, transactionID uuid.UUID) (int64, error) {
	var count int64
	err := r.db.exec.QueryRow(ctx, `SELECT count(*) FROM wallet_ledger_entries WHERE transaction_id=$1`, transactionID).Scan(&count)
	return count, err
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

// InsertIfAbsent atomically establishes the durable message identity. A
// conflict is not an error: the caller must inspect the existing row and
// compare its payload hash before deciding whether this is a safe replay.
func (r *InboxRepository) InsertIfAbsent(ctx context.Context, i InboxRecord) (bool, error) {
	tag, err := r.db.exec.Exec(ctx, `INSERT INTO inbox (consumer_name,message_id,payload_hash,received_at,completed_at) VALUES ($1,$2,$3,$4,$5) ON CONFLICT (consumer_name,message_id) DO NOTHING`, i.ConsumerName, i.MessageID, i.PayloadHash, i.ReceivedAt, i.CompletedAt)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

func (r *InboxRepository) Find(ctx context.Context, consumerName, messageID string) (InboxRecord, error) {
	var record InboxRecord
	var completedAt *time.Time
	err := r.db.exec.QueryRow(ctx, `SELECT consumer_name,message_id,payload_hash,received_at,completed_at FROM inbox WHERE consumer_name=$1 AND message_id=$2`, consumerName, messageID).Scan(&record.ConsumerName, &record.MessageID, &record.PayloadHash, &record.ReceivedAt, &completedAt)
	if completedAt != nil {
		record.CompletedAt = completedAt
	}
	return record, err
}

func (r *InboxRepository) MarkCompleted(ctx context.Context, consumerName, messageID string, completedAt time.Time) error {
	_, err := r.db.exec.Exec(ctx, `UPDATE inbox SET completed_at=$3 WHERE consumer_name=$1 AND message_id=$2`, consumerName, messageID, completedAt)
	return err
}

type OutboxRepository struct{ db *Repository }

type OutboxRecord struct {
	EventID       uuid.UUID
	OrderingID    int64
	EventType     string
	AggregateID   uuid.UUID
	CorrelationID string
	CausationID   string
	OccurredAt    time.Time
	Version       int64
	Data          []byte
	Status        string
	Attempts      int
	NextAttemptAt time.Time
	ClaimedAt     *time.Time
	ClaimToken    uuid.UUID
	PublishedAt   *time.Time
	LastError     string
}

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
func (r *OutboxRepository) CountByTypeAndCausation(ctx context.Context, eventType, causationID string) (int64, error) {
	var count int64
	err := r.db.exec.QueryRow(ctx, `SELECT count(*) FROM outbox WHERE event_type=$1 AND causation_id=$2`, eventType, causationID).Scan(&count)
	return count, err
}

func (r *OutboxRepository) FindFirstByTypeAndAggregate(ctx context.Context, eventType string, aggregateID uuid.UUID) (OutboxRecord, error) {
	var record OutboxRecord
	var correlationID, causationID, lastError *string
	var claimedAt, publishedAt *time.Time
	err := r.db.exec.QueryRow(ctx, `SELECT event_id,ordering_id,event_type,aggregate_id,correlation_id,causation_id,occurred_at,version,data,status,attempts,next_attempt_at,claimed_at,claim_token,published_at,last_error FROM outbox WHERE event_type=$1 AND aggregate_id=$2 ORDER BY ordering_id LIMIT 1`, eventType, aggregateID).Scan(
		&record.EventID, &record.OrderingID, &record.EventType, &record.AggregateID, &correlationID, &causationID, &record.OccurredAt,
		&record.Version, &record.Data, &record.Status, &record.Attempts, &record.NextAttemptAt, &claimedAt, &record.ClaimToken, &publishedAt, &lastError,
	)
	if correlationID != nil {
		record.CorrelationID = *correlationID
	}
	if causationID != nil {
		record.CausationID = *causationID
	}
	if lastError != nil {
		record.LastError = *lastError
	}
	record.ClaimedAt = claimedAt
	record.PublishedAt = publishedAt
	return record, err
}

func (r *OutboxRepository) FindByID(ctx context.Context, eventID uuid.UUID) (OutboxRecord, error) {
	var record OutboxRecord
	var correlationID, causationID, lastError *string
	var claimedAt, publishedAt *time.Time
	err := r.db.exec.QueryRow(ctx, `SELECT event_id,ordering_id,event_type,aggregate_id,correlation_id,causation_id,occurred_at,version,data,status,attempts,next_attempt_at,claimed_at,claim_token,published_at,last_error FROM outbox WHERE event_id=$1`, eventID).Scan(
		&record.EventID, &record.OrderingID, &record.EventType, &record.AggregateID, &correlationID, &causationID, &record.OccurredAt,
		&record.Version, &record.Data, &record.Status, &record.Attempts, &record.NextAttemptAt, &claimedAt, &record.ClaimToken, &publishedAt, &lastError,
	)
	if correlationID != nil {
		record.CorrelationID = *correlationID
	}
	if causationID != nil {
		record.CausationID = *causationID
	}
	if lastError != nil {
		record.LastError = *lastError
	}
	record.ClaimedAt = claimedAt
	record.PublishedAt = publishedAt
	return record, err
}
func nullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
}
func (r *OutboxRepository) ClaimOne(ctx context.Context, now time.Time) (uuid.UUID, error) {
	record, err := r.ClaimOneRecord(ctx, now, 30*time.Second)
	return record.EventID, err
}

func (r *OutboxRepository) ClaimOneRecord(ctx context.Context, now time.Time, lease time.Duration) (OutboxRecord, error) {
	var record OutboxRecord
	var correlationID, causationID, lastError *string
	var claimedAt, publishedAt *time.Time
	claimToken := uuid.New()
	err := r.db.exec.QueryRow(ctx, `
		UPDATE outbox
		SET status='CLAIMED', claimed_at=$1, claim_token=$3, attempts=attempts+1
		WHERE event_id=(
			SELECT event_id FROM outbox
			WHERE ((status='PENDING' AND next_attempt_at <= $1)
			   OR (status='CLAIMED' AND claimed_at <= $2))
			  AND NOT EXISTS (
				SELECT 1 FROM outbox predecessor
				WHERE predecessor.aggregate_id = outbox.aggregate_id
				  AND predecessor.ordering_id < outbox.ordering_id
				  AND predecessor.status <> 'PUBLISHED'
			  )
			ORDER BY next_attempt_at, event_id
			FOR UPDATE SKIP LOCKED LIMIT 1
		)
		RETURNING event_id,ordering_id,event_type,aggregate_id,correlation_id,causation_id,occurred_at,version,data,status,attempts,next_attempt_at,claimed_at,claim_token,published_at,last_error`, now, now.Add(-lease), claimToken).Scan(
		&record.EventID, &record.OrderingID, &record.EventType, &record.AggregateID, &correlationID, &causationID, &record.OccurredAt,
		&record.Version, &record.Data, &record.Status, &record.Attempts, &record.NextAttemptAt, &claimedAt, &record.ClaimToken, &publishedAt, &lastError,
	)
	if correlationID != nil {
		record.CorrelationID = *correlationID
	}
	if causationID != nil {
		record.CausationID = *causationID
	}
	if lastError != nil {
		record.LastError = *lastError
	}
	record.ClaimedAt = claimedAt
	record.PublishedAt = publishedAt
	return record, err
}

// ClaimRecordByID is used by deterministic integration tests to coordinate a
// specific claim without weakening the production claim protocol.
func (r *OutboxRepository) ClaimRecordByID(ctx context.Context, eventID uuid.UUID, now time.Time, lease time.Duration) (OutboxRecord, error) {
	var record OutboxRecord
	var correlationID, causationID, lastError *string
	var claimedAt, publishedAt *time.Time
	claimToken := uuid.New()
	err := r.db.exec.QueryRow(ctx, `
		UPDATE outbox
		SET status='CLAIMED', claimed_at=$2, claim_token=$3, attempts=attempts+1
		WHERE event_id=$1
		  AND ((status='PENDING' AND next_attempt_at <= $2)
		       OR (status='CLAIMED' AND claimed_at <= $4))
		  AND NOT EXISTS (
			SELECT 1 FROM outbox predecessor
			WHERE predecessor.aggregate_id = outbox.aggregate_id
			  AND predecessor.ordering_id < outbox.ordering_id
			  AND predecessor.status <> 'PUBLISHED'
		  )
		RETURNING event_id,ordering_id,event_type,aggregate_id,correlation_id,causation_id,occurred_at,version,data,status,attempts,next_attempt_at,claimed_at,claim_token,published_at,last_error`, eventID, now, claimToken, now.Add(-lease)).Scan(
		&record.EventID, &record.OrderingID, &record.EventType, &record.AggregateID, &correlationID, &causationID, &record.OccurredAt,
		&record.Version, &record.Data, &record.Status, &record.Attempts, &record.NextAttemptAt, &claimedAt, &record.ClaimToken, &publishedAt, &lastError,
	)
	if correlationID != nil {
		record.CorrelationID = *correlationID
	}
	if causationID != nil {
		record.CausationID = *causationID
	}
	if lastError != nil {
		record.LastError = *lastError
	}
	record.ClaimedAt = claimedAt
	record.PublishedAt = publishedAt
	return record, err
}

func (r *OutboxRepository) MarkPublished(ctx context.Context, eventID, claimToken uuid.UUID, publishedAt time.Time) error {
	tag, err := r.db.exec.Exec(ctx, `UPDATE outbox SET status='PUBLISHED', published_at=$3, claimed_at=NULL, claim_token=NULL, last_error=NULL WHERE event_id=$1 AND claim_token=$2 AND status='CLAIMED'`, eventID, claimToken, publishedAt)
	if err == nil && tag.RowsAffected() != 1 {
		return ErrOutboxClaimLost
	}
	return err
}

func (r *OutboxRepository) MarkRetry(ctx context.Context, eventID, claimToken uuid.UUID, attempts int, nextAttemptAt time.Time, lastError string, maxAttempts int, now time.Time) error {
	status := "PENDING"
	if attempts >= maxAttempts {
		status = "FAILED"
	}
	tag, err := r.db.exec.Exec(ctx, `UPDATE outbox SET status=$2, next_attempt_at=$3, claimed_at=NULL, claim_token=NULL, last_error=$4 WHERE event_id=$1 AND claim_token=$5 AND status='CLAIMED'`, eventID, status, nextAttemptAt, nullableString(lastError), claimToken)
	if err == nil && tag.RowsAffected() != 1 {
		return ErrOutboxClaimLost
	}
	return err
}
