package financial

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/leonardodacosta/distributedBettingProcessing/internal/domain/wager"
	"github.com/leonardodacosta/distributedBettingProcessing/internal/infrastructure/postgres"
	"github.com/leonardodacosta/distributedBettingProcessing/internal/observability"
)

func TestPendingReferenceResolvesAfterReferenceArrives(t *testing.T) {
	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		t.Skip("DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	db, err := postgres.NewRepository(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	now := time.Now().UTC()
	metrics := observability.NewMetrics()
	service := NewService(db, metrics)
	walletID := uuid.New()
	playerID := "reference-player-" + walletID.String()
	if err := service.OpenWallet(ctx, walletID, playerID, moneyMust("100.00", "BRL"), now); err != nil {
		t.Fatal(err)
	}
	provider := "reference-provider-" + walletID.String()
	referenceExternal := "future-bet-" + walletID.String()
	pendingCommand := validCommand(walletID, uuid.New(), "refund-before-reference-"+walletID.String(), wager.Refund, moneyMust("20.00", "BRL"))
	pendingCommand.ProviderID, pendingCommand.PlayerID, pendingCommand.GameID, pendingCommand.RoundID = provider, playerID, "reference-game", "reference-round"
	pendingCommand.ReferenceExternalID = referenceExternal
	result, err := service.Process(ctx, pendingCommand, now)
	if err != nil || result.State != wager.PendingReference || result.Balance != 10000 {
		t.Fatalf("pending reversal = %+v, error=%v", result, err)
	}

	repository := postgres.NewWagerTransactionRepository(db)
	record, err := repository.FindByID(ctx, pendingCommand.ID)
	if err != nil || record.ReferenceAttempts != 0 || record.ReferenceNextAttemptAt == nil {
		t.Fatalf("initial pending metadata = %+v, error=%v", record, err)
	}
	if err := eventuallyReference(ctx, func() (bool, error) {
		_, processErr := service.ProcessPendingReference(ctx, time.Now().UTC().Add(time.Second), 3, 0)
		if processErr != nil {
			return false, processErr
		}
		current, findErr := repository.FindByID(ctx, pendingCommand.ID)
		return findErr == nil && current.State == string(wager.PendingReference) && current.ReferenceAttempts == 1, findErr
	}); err != nil {
		t.Fatalf("missing-reference retry: %v", err)
	}
	if got := metrics.Render(); !strings.Contains(got, `wager_retry_total{component="pending_reference"}`) {
		t.Fatalf("pending-reference retry was not observed after durable retry scheduling: %s", got)
	}

	bet := validCommand(walletID, uuid.New(), referenceExternal, wager.Bet, moneyMust("20.00", "BRL"))
	bet.ProviderID, bet.PlayerID, bet.GameID, bet.RoundID = provider, playerID, pendingCommand.GameID, pendingCommand.RoundID
	if _, err := service.Process(ctx, bet, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := eventuallyReference(ctx, func() (bool, error) {
		_, processErr := service.ProcessPendingReference(ctx, time.Now().UTC().Add(time.Second), 3, 0)
		if processErr != nil {
			return false, processErr
		}
		current, findErr := repository.FindByID(ctx, pendingCommand.ID)
		return findErr == nil && current.State == string(wager.Processed) && current.FailureCode == "" && current.ReferenceNextAttemptAt == nil, findErr
	}); err != nil {
		t.Fatalf("resolved-reference attempt: %v", err)
	}
	record, err = repository.FindByID(ctx, pendingCommand.ID)
	if err != nil {
		t.Fatal(err)
	}
	if wallet, err := postgres.NewWalletRepository(db).Find(ctx, walletID); err != nil || wallet.Balance != 10000 {
		t.Fatalf("resolved wallet = %+v, error=%v", wallet, err)
	}
	if count, err := postgres.NewLedgerRepository(db).CountByTransaction(ctx, pendingCommand.ID); err != nil || count != 1 {
		t.Fatalf("resolved reversal ledger entries = %d, error=%v", count, err)
	}
	if count, err := postgres.NewOutboxRepository(db).CountByTypeAndAggregate(ctx, "WagerTransactionPendingReference", pendingCommand.ID); err != nil || count != 1 {
		t.Fatalf("pending event count = %d, error=%v", count, err)
	}
	if count, err := postgres.NewOutboxRepository(db).CountByTypeAndAggregate(ctx, "WagerTransactionProcessed", pendingCommand.ID); err != nil || count != 1 {
		t.Fatalf("processed event count = %d, error=%v", count, err)
	}
}

func TestReferenceWorkerResumesPendingWorkAfterRestart(t *testing.T) {
	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		t.Skip("DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	db, err := postgres.NewRepository(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	dbWorkerB, err := postgres.NewRepository(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer dbWorkerB.Close()
	dbWorkerC, err := postgres.NewRepository(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer dbWorkerC.Close()

	now := time.Now().UTC()
	service := NewService(db)
	walletID := uuid.New()
	playerID := "reference-restart-player-" + walletID.String()
	if err := service.OpenWallet(ctx, walletID, playerID, moneyMust("100.00", "BRL"), now); err != nil {
		t.Fatal(err)
	}
	provider := "reference-restart-provider-" + walletID.String()
	referenceExternal := "restart-future-bet-" + walletID.String()
	pending := validCommand(walletID, uuid.New(), "restart-refund-"+walletID.String(), wager.Refund, moneyMust("10.00", "BRL"))
	pending.ProviderID, pending.PlayerID, pending.GameID, pending.RoundID = provider, playerID, "restart-game", "restart-round"
	pending.ReferenceExternalID = referenceExternal
	if result, err := service.Process(ctx, pending, now); err != nil || result.State != wager.PendingReference {
		t.Fatalf("pending restart fixture = %+v, error=%v", result, err)
	}

	workerA := NewReferenceWorker(service, ReferenceWorkerConfig{PollInterval: 10 * time.Millisecond, MaxAttempts: 10, Backoff: 10 * time.Millisecond})
	if err := workerA.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		record, findErr := postgres.NewWagerTransactionRepository(db).FindByID(ctx, pending.ID)
		if findErr == nil && record.ReferenceAttempts > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("worker did not persist a retry before restart: record=%+v error=%v", record, findErr)
		}
		time.Sleep(10 * time.Millisecond)
	}
	stopCtx, stopCancel := context.WithTimeout(ctx, time.Second)
	if err := workerA.Stop(stopCtx); err != nil {
		stopCancel()
		t.Fatal(err)
	}
	stopCancel()

	bet := validCommand(walletID, uuid.New(), referenceExternal, wager.Bet, moneyMust("10.00", "BRL"))
	bet.ProviderID, bet.PlayerID, bet.GameID, bet.RoundID = provider, playerID, pending.GameID, pending.RoundID
	if _, err := service.Process(ctx, bet, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	workerB := NewReferenceWorker(NewService(dbWorkerB), ReferenceWorkerConfig{PollInterval: 10 * time.Millisecond, MaxAttempts: 10, Backoff: 10 * time.Millisecond})
	workerC := NewReferenceWorker(NewService(dbWorkerC), ReferenceWorkerConfig{PollInterval: 10 * time.Millisecond, MaxAttempts: 10, Backoff: 10 * time.Millisecond})
	if err := workerB.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := workerC.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), time.Second)
		defer stopCancel()
		if err := workerB.Stop(stopCtx); err != nil {
			t.Errorf("stop restarted worker: %v", err)
		}
		if err := workerC.Stop(stopCtx); err != nil {
			t.Errorf("stop concurrent worker: %v", err)
		}
	}()
	if err := eventuallyReference(ctx, func() (bool, error) {
		record, findErr := postgres.NewWagerTransactionRepository(db).FindByID(ctx, pending.ID)
		if errors.Is(findErr, pgx.ErrNoRows) {
			return false, nil
		}
		return findErr == nil && record.State == string(wager.Processed), findErr
	}); err != nil {
		t.Fatalf("restart worker did not resolve pending reference: %v", err)
	}
	if count, err := postgres.NewLedgerRepository(db).CountByTransaction(ctx, pending.ID); err != nil || count != 1 {
		t.Fatalf("concurrent workers ledger entries = %d, error=%v", count, err)
	}
}

func TestReferenceWorkerSerializesBoundaryAgainstReferenceCommit(t *testing.T) {
	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		t.Skip("DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	dbPending, err := postgres.NewRepository(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer dbPending.Close()
	dbReference, err := postgres.NewRepository(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer dbReference.Close()
	dbWorker, err := postgres.NewRepository(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer dbWorker.Close()

	service := NewService(dbPending)
	walletID := uuid.New()
	playerID := "reference-boundary-player-" + walletID.String()
	provider := "reference-boundary-provider-" + walletID.String()
	if err := service.OpenWallet(ctx, walletID, playerID, moneyMust("100.00", "BRL"), time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	referenceExternal := "boundary-reference-" + walletID.String()
	pending := validCommand(walletID, uuid.New(), "boundary-refund-"+walletID.String(), wager.Refund, moneyMust("20.00", "BRL"))
	pending.ProviderID, pending.PlayerID, pending.GameID, pending.RoundID = provider, playerID, "boundary-game", "boundary-round"
	pending.ReferenceExternalID = referenceExternal
	if result, err := service.Process(ctx, pending, time.Now().UTC()); err != nil || result.State != wager.PendingReference {
		t.Fatalf("pending boundary fixture = %+v, error=%v", result, err)
	}

	identityLocked := make(chan struct{})
	allowReferenceCommit := make(chan struct{})
	referenceCommitted := make(chan error, 1)
	go func() {
		referenceCommitted <- dbReference.WithTx(ctx, func(ctx context.Context, tx *postgres.Repository) error {
			repository := postgres.NewWagerTransactionRepository(tx)
			if err := repository.LockReferenceIdentity(ctx, provider, referenceExternal); err != nil {
				return err
			}
			close(identityLocked)
			<-allowReferenceCommit
			referenceCommand := validCommand(walletID, uuid.New(), referenceExternal, wager.Bet, moneyMust("20.00", "BRL"))
			referenceCommand.ProviderID, referenceCommand.PlayerID, referenceCommand.GameID, referenceCommand.RoundID = provider, playerID, pending.GameID, pending.RoundID
			result, err := marshalResult(Result{TransactionID: referenceCommand.ID, State: wager.Processed, Balance: 8000, Amount: referenceCommand.Amount})
			if err != nil {
				return err
			}
			return repository.Insert(ctx, postgres.WagerTransactionRecord{
				ID: referenceCommand.ID, ExternalID: referenceCommand.ExternalID, ProviderID: referenceCommand.ProviderID,
				WalletID: referenceCommand.WalletID, PlayerID: referenceCommand.PlayerID, GameID: referenceCommand.GameID,
				RoundID: referenceCommand.RoundID, Type: string(referenceCommand.Type), Amount: referenceCommand.Amount.Minor(),
				Currency: referenceCommand.Amount.Currency(), State: string(wager.Processed), IdempotencyKey: referenceCommand.IdempotencyKey,
				PayloadHash: "boundary-reference-hash", Result: result, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
			})
		})
	}()
	<-identityLocked

	workerDone := make(chan error, 1)
	go func() {
		_, processErr := NewService(dbWorker).ProcessPendingReference(ctx, time.Now().UTC().Add(time.Second), 1, 0)
		workerDone <- processErr
	}()
	select {
	case err := <-workerDone:
		t.Fatalf("worker decided before reference transaction committed: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	close(allowReferenceCommit)
	if err := <-referenceCommitted; err != nil {
		t.Fatal(err)
	}
	if err := <-workerDone; err != nil {
		t.Fatal(err)
	}
	record, err := postgres.NewWagerTransactionRepository(dbPending).FindByID(ctx, pending.ID)
	if err != nil || record.State != string(wager.Processed) || record.FailureCode != "" {
		t.Fatalf("boundary result = %+v, error=%v", record, err)
	}
	if count, err := postgres.NewLedgerRepository(dbPending).CountByTransaction(ctx, pending.ID); err != nil || count != 1 {
		t.Fatalf("boundary ledger entries = %d, error=%v", count, err)
	}
}

func TestPendingReferenceExhaustionIsRejectedWithStableFailureCode(t *testing.T) {
	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		t.Skip("DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	db, err := postgres.NewRepository(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	now := time.Now().UTC()
	service := NewService(db)
	walletID := uuid.New()
	playerID := "reference-exhaustion-player-" + walletID.String()
	if err := service.OpenWallet(ctx, walletID, playerID, moneyMust("100.00", "BRL"), now); err != nil {
		t.Fatal(err)
	}
	command := validCommand(walletID, uuid.New(), "exhausted-refund-"+walletID.String(), wager.Refund, moneyMust("5.00", "BRL"))
	command.PlayerID = playerID
	command.ReferenceExternalID = "never-arrives-" + walletID.String()
	if _, err := service.Process(ctx, command, now); err != nil {
		t.Fatal(err)
	}
	if err := eventuallyReference(ctx, func() (bool, error) {
		_, processErr := service.ProcessPendingReference(ctx, time.Now().UTC().Add(time.Second), 2, 0)
		if processErr != nil {
			return false, processErr
		}
		current, findErr := postgres.NewWagerTransactionRepository(db).FindByID(ctx, command.ID)
		return findErr == nil && current.State == string(wager.Rejected), findErr
	}); err != nil {
		t.Fatalf("reference exhaustion: %v", err)
	}
	record, err := postgres.NewWagerTransactionRepository(db).FindByID(ctx, command.ID)
	if err != nil || record.State != string(wager.Rejected) || record.FailureCode != FailureCodeReferenceNotFound || record.ReferenceNextAttemptAt != nil || record.ReferenceAttempts != 2 {
		t.Fatalf("exhausted transaction = %+v, error=%v", record, err)
	}
	if wallet, err := postgres.NewWalletRepository(db).Find(ctx, walletID); err != nil || wallet.Balance != 10000 {
		t.Fatalf("exhausted wallet = %+v, error=%v", wallet, err)
	}
	if count, err := postgres.NewLedgerRepository(db).CountByTransaction(ctx, command.ID); err != nil || count != 0 {
		t.Fatalf("exhausted ledger entries = %d, error=%v", count, err)
	}
	if count, err := postgres.NewOutboxRepository(db).CountByTypeAndAggregate(ctx, "WagerTransactionRejected", command.ID); err != nil || count != 1 {
		t.Fatalf("rejection event count = %d, error=%v", count, err)
	}
}

func eventuallyReference(ctx context.Context, fn func() (bool, error)) error {
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		ok, err := fn()
		if err != nil {
			return err
		}
		if ok {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}
