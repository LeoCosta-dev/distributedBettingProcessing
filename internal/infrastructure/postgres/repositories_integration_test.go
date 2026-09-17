package postgres

import (
	"context"
	"errors"
	"github.com/google/uuid"
	"os"
	"testing"
	"time"
)

func TestRepositoriesAgainstPostgres(t *testing.T) {
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		t.Skip("DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	db, err := NewRepository(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	walletID, txID := uuid.New(), uuid.New()
	now := time.Now().UTC()
	wallets := NewWalletRepository(db)
	if err := wallets.Insert(ctx, WalletRecord{ID: walletID, PlayerID: "integration-player-" + walletID.String(), Currency: "BRL", Balance: 10000, Version: 1, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	w, err := wallets.Find(ctx, walletID)
	if err != nil || w.Balance != 10000 {
		t.Fatalf("wallet: %+v, %v", w, err)
	}
	transactions := NewWagerTransactionRepository(db)
	if err := transactions.Insert(ctx, WagerTransactionRecord{ID: txID, ExternalID: "integration-external-" + txID.String(), ProviderID: "integration-provider", WalletID: walletID, Type: "BET", Amount: 8000, Currency: "BRL", State: "PROCESSED", IdempotencyKey: "integration-idem-" + txID.String(), PayloadHash: "integration-hash", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if got, err := transactions.FindByExternal(ctx, "integration-provider", "integration-external-"+txID.String()); err != nil || got.ID != txID {
		t.Fatalf("transaction: %+v, %v", got, err)
	}
	if err := NewLedgerRepository(db).Insert(ctx, LedgerRecord{ID: uuid.New(), WalletID: walletID, TransactionID: txID, Direction: "DEBIT", Value: 8000, Currency: "BRL", BalanceBefore: 10000, BalanceAfter: 2000, Timestamp: now}); err != nil {
		t.Fatal(err)
	}
	if n, err := NewLedgerRepository(db).Count(ctx, walletID); err != nil || n != 1 {
		t.Fatalf("ledger count: %d, %v", n, err)
	}
	if err := NewInboxRepository(db).Insert(ctx, InboxRecord{ConsumerName: "integration-consumer", MessageID: "integration-message-" + walletID.String(), PayloadHash: "integration-hash", ReceivedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := NewOutboxRepository(db).Insert(ctx, uuid.New(), "TestEvent", walletID, now, []byte(`{"ok":true}`), now); err != nil {
		t.Fatal(err)
	}
	if _, err := NewOutboxRepository(db).ClaimOne(ctx, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	rollbackErr := errors.New("force rollback")
	if err := db.WithTx(ctx, func(ctx context.Context, tx *Repository) error {
		if err := NewInboxRepository(tx).Insert(ctx, InboxRecord{ConsumerName: "rollback-consumer", MessageID: "rollback-message", PayloadHash: "hash", ReceivedAt: now}); err != nil {
			return err
		}
		return rollbackErr
	}); !errors.Is(err, rollbackErr) {
		t.Fatalf("rollback: %v", err)
	}
	var rollbackCount int
	if err := db.pool.QueryRow(ctx, `SELECT count(*) FROM inbox WHERE consumer_name='rollback-consumer' AND message_id='rollback-message'`).Scan(&rollbackCount); err != nil || rollbackCount != 0 {
		t.Fatalf("rollback persisted data: %d, %v", rollbackCount, err)
	}
	commitMessage := "commit-message-" + walletID.String()
	if err := db.WithTx(ctx, func(ctx context.Context, tx *Repository) error {
		return NewInboxRepository(tx).Insert(ctx, InboxRecord{ConsumerName: "commit-consumer", MessageID: commitMessage, PayloadHash: "hash", ReceivedAt: now})
	}); err != nil {
		t.Fatal(err)
	}
	var commitCount int
	if err := db.pool.QueryRow(ctx, `SELECT count(*) FROM inbox WHERE consumer_name='commit-consumer' AND message_id=$1`, commitMessage).Scan(&commitCount); err != nil || commitCount != 1 {
		t.Fatalf("commit not persisted: %d, %v", commitCount, err)
	}
}
