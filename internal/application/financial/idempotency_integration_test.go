package financial

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/leonardodacosta/distributedBettingProcessing/internal/domain/wager"
	"github.com/leonardodacosta/distributedBettingProcessing/internal/infrastructure/postgres"
)

func TestPersistentIdempotencyAcrossReplayRestartAndInstances(t *testing.T) {
	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		t.Skip("DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	db, err := postgres.NewRepository(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	service := NewService(db)
	now := time.Now().UTC()
	walletID := uuid.New()
	opening := moneyMust("100.00", "BRL")
	if err := service.OpenWallet(ctx, walletID, "idempotency-player-"+walletID.String(), opening, now); err != nil {
		db.Close()
		t.Fatal(err)
	}
	missingKey := validCommand(walletID, uuid.New(), "idempotent-missing-key-"+walletID.String(), wager.Bet, moneyMust("1.00", "BRL"))
	missingKey.PlayerID = "idempotency-player-" + walletID.String()
	missingKey.IdempotencyKey = ""
	if _, err := service.Process(ctx, missingKey, now); !errors.Is(err, ErrInvalidCommand) {
		db.Close()
		t.Fatalf("missing Idempotency-Key error = %v", err)
	}
	if _, err := postgres.NewWagerTransactionRepository(db).FindByExternal(ctx, missingKey.ProviderID, missingKey.ExternalID); !errors.Is(err, pgx.ErrNoRows) {
		db.Close()
		t.Fatalf("missing Idempotency-Key was persisted: %v", err)
	}

	amount := moneyMust("10.00", "BRL")
	command := validCommand(walletID, uuid.New(), "idempotent-bet-"+walletID.String(), wager.Bet, amount)
	command.PlayerID = "idempotency-player-" + walletID.String()
	first, err := service.Process(ctx, command, now)
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	if first.State != wager.Processed || first.Balance != 9000 {
		db.Close()
		t.Fatalf("first result: %+v", first)
	}
	record, err := postgres.NewWagerTransactionRepository(db).FindByExternal(ctx, command.ProviderID, command.ExternalID)
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	expectedHash, err := canonicalPayloadHash(command)
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	if record.PayloadHash != expectedHash || len(record.Result) == 0 {
		db.Close()
		t.Fatalf("idempotency snapshot was not persisted: hash=%q result=%s", record.PayloadHash, record.Result)
	}

	secondCommand := validCommand(walletID, uuid.New(), "idempotent-second-"+walletID.String(), wager.Bet, moneyMust("5.00", "BRL"))
	secondCommand.PlayerID = "idempotency-player-" + walletID.String()
	if second, err := service.Process(ctx, secondCommand, now); err != nil || second.Balance != 8500 {
		db.Close()
		t.Fatalf("second operation: %+v %v", second, err)
	}

	replayCommand := command
	replayCommand.ID = uuid.New()
	replayCommand.PayloadHash = "caller-supplied-value-is-ignored"
	replay, err := service.Process(ctx, replayCommand, now)
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	if replay != first {
		db.Close()
		t.Fatalf("replay = %+v, original = %+v", replay, first)
	}
	if wallet, err := postgres.NewWalletRepository(db).Find(ctx, walletID); err != nil || wallet.Balance != 8500 {
		db.Close()
		t.Fatalf("replay changed wallet: %+v %v", wallet, err)
	}

	conflictingKey := command
	conflictingKey.ID = uuid.New()
	conflictingKey.Amount = moneyMust("11.00", "BRL")
	if _, err := service.Process(ctx, conflictingKey, now); !errors.Is(err, ErrIdempotencyConflict) {
		db.Close()
		t.Fatalf("same key/different payload error = %v", err)
	}
	conflictingExternal := command
	conflictingExternal.ID = uuid.New()
	conflictingExternal.IdempotencyKey = "different-key-" + walletID.String()
	if _, err := service.Process(ctx, conflictingExternal, now); !errors.Is(err, ErrIdempotencyConflict) {
		db.Close()
		t.Fatalf("same transaction/different key error = %v", err)
	}
	conflictingID := command
	conflictingID.ExternalID = "different-external-" + walletID.String()
	conflictingID.IdempotencyKey = "different-id-key-" + walletID.String()
	if _, err := service.Process(ctx, conflictingID, now); !errors.Is(err, ErrIdempotencyConflict) {
		db.Close()
		t.Fatalf("same ID/different key error = %v", err)
	}
	if wallet, err := postgres.NewWalletRepository(db).Find(ctx, walletID); err != nil || wallet.Balance != 8500 {
		db.Close()
		t.Fatalf("conflict changed wallet: %+v %v", wallet, err)
	}
	historical := validCommand(walletID, uuid.New(), "idempotent-historical-"+walletID.String(), wager.Bet, moneyMust("1.00", "BRL"))
	historical.PlayerID = "idempotency-player-" + walletID.String()
	historicalHash, err := canonicalPayloadHash(historical)
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	historical.PayloadHash = historicalHash
	historicalID := historical.ID
	if err := postgres.NewWagerTransactionRepository(db).Insert(ctx, postgres.WagerTransactionRecord{
		ID: historicalID, ExternalID: historical.ExternalID, ProviderID: historical.ProviderID, WalletID: historical.WalletID,
		PlayerID: historical.PlayerID, GameID: historical.GameID, RoundID: historical.RoundID, Type: string(historical.Type), Amount: historical.Amount.Minor(),
		Currency: historical.Amount.Currency(), State: string(wager.Processed), IdempotencyKey: historical.IdempotencyKey,
		PayloadHash: "pre-loop4-hash", CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		db.Close()
		t.Fatal(err)
	}
	historicalRetry := historical
	historicalRetry.ID = uuid.New()
	if _, err := service.Process(ctx, historicalRetry, now); !errors.Is(err, ErrReplayUnavailable) {
		db.Close()
		t.Fatalf("historical record without compatible snapshot error = %v", err)
	}
	if _, err := postgres.NewWagerTransactionRepository(db).FindByID(ctx, historicalRetry.ID); !errors.Is(err, pgx.ErrNoRows) {
		db.Close()
		t.Fatalf("historical retry was reprocessed: %v", err)
	}
	if wallet, err := postgres.NewWalletRepository(db).Find(ctx, walletID); err != nil || wallet.Balance != 8500 {
		db.Close()
		t.Fatalf("historical retry changed wallet: %+v %v", wallet, err)
	}

	db.Close()
	db, err = postgres.NewRepository(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	service = NewService(db)
	restarted, err := service.Process(ctx, replayCommand, now)
	if err != nil || restarted != first {
		db.Close()
		t.Fatalf("replay after restart = %+v %v, original = %+v", restarted, err, first)
	}

	otherDB, err := postgres.NewRepository(ctx, databaseURL)
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	otherService := NewService(otherDB)
	crossInstance, err := otherService.Process(ctx, replayCommand, now)
	if err != nil || crossInstance != first {
		otherDB.Close()
		db.Close()
		t.Fatalf("cross-instance replay = %+v %v, original = %+v", crossInstance, err, first)
	}
	otherDB.Close()
	db.Close()

	concurrentDB, err := postgres.NewRepository(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer concurrentDB.Close()
	concurrentService := NewService(concurrentDB)
	concurrentWalletID := uuid.New()
	if err := concurrentService.OpenWallet(ctx, concurrentWalletID, "idempotency-concurrent-"+concurrentWalletID.String(), opening, now); err != nil {
		t.Fatal(err)
	}
	concurrentCommand := validCommand(concurrentWalletID, uuid.New(), "idempotent-concurrent-"+concurrentWalletID.String(), wager.Bet, amount)
	concurrentCommand.PlayerID = "idempotency-concurrent-" + concurrentWalletID.String()
	type outcome struct {
		result Result
		err    error
	}
	outcomes := make(chan outcome, 50)
	var waitGroup sync.WaitGroup
	for i := 0; i < 50; i++ {
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			result, err := concurrentService.Process(ctx, concurrentCommand, now)
			outcomes <- outcome{result: result, err: err}
		}()
	}
	waitGroup.Wait()
	close(outcomes)
	for outcome := range outcomes {
		if outcome.err != nil {
			t.Fatalf("concurrent duplicate: %v", outcome.err)
		}
		if outcome.result.TransactionID != concurrentCommand.ID || outcome.result.State != wager.Processed || outcome.result.Balance != 9000 || outcome.result.Amount != concurrentCommand.Amount {
			t.Fatalf("concurrent duplicate result: %+v", outcome.result)
		}
	}
	wallet, err := postgres.NewWalletRepository(concurrentDB).Find(ctx, concurrentWalletID)
	if err != nil {
		t.Fatal(err)
	}
	if wallet.Balance != 9000 {
		t.Fatalf("concurrent duplicates changed balance more than once: %d", wallet.Balance)
	}
	transactionRepository := postgres.NewWagerTransactionRepository(concurrentDB)
	transactionCount, err := transactionRepository.CountByExternal(ctx, concurrentCommand.ProviderID, concurrentCommand.ExternalID)
	if err != nil {
		t.Fatal(err)
	}
	if transactionCount != 1 {
		t.Fatalf("concurrent duplicate transactions = %d, want 1", transactionCount)
	}
	idempotencyCount, err := transactionRepository.CountByIdempotency(ctx, concurrentCommand.ProviderID, concurrentCommand.IdempotencyKey)
	if err != nil || idempotencyCount != 1 {
		t.Fatalf("concurrent duplicate idempotency records = %d: %v", idempotencyCount, err)
	}
	ledgerRepository := postgres.NewLedgerRepository(concurrentDB)
	ledgerCount, err := ledgerRepository.CountByTransaction(ctx, concurrentCommand.ID)
	if err != nil {
		t.Fatal(err)
	}
	if ledgerCount != 1 {
		t.Fatalf("concurrent duplicate ledger entries = %d, want 1", ledgerCount)
	}
	outboxRepository := postgres.NewOutboxRepository(concurrentDB)
	if eventCount, err := outboxRepository.CountByTypeAndAggregate(ctx, "WagerTransactionProcessed", concurrentCommand.ID); err != nil || eventCount != 1 {
		t.Fatalf("concurrent duplicate processed events = %d, want 1: %v", eventCount, err)
	}
	if eventCount, err := outboxRepository.CountByTypeAndCausation(ctx, "WalletBalanceChanged", concurrentCommand.ID.String()); err != nil || eventCount != 1 {
		t.Fatalf("concurrent duplicate balance events = %d, want 1: %v", eventCount, err)
	}
}

func TestIdempotencyReplaysRejectedAndPendingReference(t *testing.T) {
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
	service := NewService(db)
	now := time.Now().UTC()
	walletID := uuid.New()
	playerID := "idempotency-states-" + walletID.String()
	if err := service.OpenWallet(ctx, walletID, playerID, moneyMust("100.00", "BRL"), now); err != nil {
		t.Fatal(err)
	}

	rejectedCommand := validCommand(walletID, uuid.New(), "idempotency-rejected-"+walletID.String(), wager.Bet, moneyMust("101.00", "BRL"))
	rejectedCommand.PlayerID = playerID
	rejected, err := service.Process(ctx, rejectedCommand, now)
	if err != nil || rejected.State != wager.Rejected || rejected.Balance != 10000 {
		t.Fatalf("rejected operation: %+v %v", rejected, err)
	}
	rejectedReplayCommand := rejectedCommand
	rejectedReplayCommand.ID = uuid.New()
	rejectedReplayCommand.PayloadHash = "ignored"
	rejectedReplay, err := service.Process(ctx, rejectedReplayCommand, now)
	if err != nil || rejectedReplay != rejected {
		t.Fatalf("rejected replay = %+v %v, original = %+v", rejectedReplay, err, rejected)
	}
	transactionRepository := postgres.NewWagerTransactionRepository(db)
	if count, err := transactionRepository.CountByExternal(ctx, rejectedCommand.ProviderID, rejectedCommand.ExternalID); err != nil || count != 1 {
		t.Fatalf("rejected transaction count = %d: %v", count, err)
	}
	if count, err := postgres.NewLedgerRepository(db).CountByTransaction(ctx, rejectedCommand.ID); err != nil || count != 0 {
		t.Fatalf("rejected ledger count = %d: %v", count, err)
	}
	if count, err := postgres.NewOutboxRepository(db).CountByTypeAndAggregate(ctx, "WagerTransactionRejected", rejectedCommand.ID); err != nil || count != 1 {
		t.Fatalf("rejected event count = %d: %v", count, err)
	}
	if count, err := postgres.NewOutboxRepository(db).CountByTypeAndCausation(ctx, "WagerTransactionProcessed", rejectedCommand.ID.String()); err != nil || count != 0 {
		t.Fatalf("rejected processed event count = %d: %v", count, err)
	}
	if count, err := postgres.NewOutboxRepository(db).CountByTypeAndCausation(ctx, "WalletBalanceChanged", rejectedCommand.ID.String()); err != nil || count != 0 {
		t.Fatalf("rejected balance event count = %d: %v", count, err)
	}

	pendingCommand := validCommand(walletID, uuid.New(), "idempotency-pending-"+walletID.String(), wager.Refund, moneyMust("1.00", "BRL"))
	pendingCommand.PlayerID = playerID
	pending, err := service.Process(ctx, pendingCommand, now)
	if err != nil || pending.State != wager.PendingReference || pending.Balance != 10000 {
		t.Fatalf("pending operation: %+v %v", pending, err)
	}
	pendingReplayCommand := pendingCommand
	pendingReplayCommand.ID = uuid.New()
	pendingReplayCommand.PayloadHash = "ignored"
	pendingReplay, err := service.Process(ctx, pendingReplayCommand, now)
	if err != nil || pendingReplay != pending {
		t.Fatalf("pending replay = %+v %v, original = %+v", pendingReplay, err, pending)
	}
	if count, err := transactionRepository.CountByExternal(ctx, pendingCommand.ProviderID, pendingCommand.ExternalID); err != nil || count != 1 {
		t.Fatalf("pending transaction count = %d: %v", count, err)
	}
	if count, err := postgres.NewLedgerRepository(db).CountByTransaction(ctx, pendingCommand.ID); err != nil || count != 0 {
		t.Fatalf("pending ledger count = %d: %v", count, err)
	}
	if count, err := postgres.NewOutboxRepository(db).CountByTypeAndAggregate(ctx, "WagerTransactionPendingReference", pendingCommand.ID); err != nil || count != 1 {
		t.Fatalf("pending event count = %d: %v", count, err)
	}
	if count, err := postgres.NewOutboxRepository(db).CountByTypeAndCausation(ctx, "WagerTransactionProcessed", pendingCommand.ID.String()); err != nil || count != 0 {
		t.Fatalf("pending processed event count = %d: %v", count, err)
	}
	if count, err := postgres.NewOutboxRepository(db).CountByTypeAndCausation(ctx, "WalletBalanceChanged", pendingCommand.ID.String()); err != nil || count != 0 {
		t.Fatalf("pending balance event count = %d: %v", count, err)
	}
	if wallet, err := postgres.NewWalletRepository(db).Find(ctx, walletID); err != nil || wallet.Balance != 10000 {
		t.Fatalf("state replays changed wallet: %+v %v", wallet, err)
	}
}

func TestConcurrentConflictingIdempotencyResolvesUniqueViolation(t *testing.T) {
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
	service := NewService(db)
	now := time.Now().UTC()
	walletA := uuid.New()
	walletB := uuid.New()
	playerA := "idempotency-conflict-a-" + walletA.String()
	playerB := "idempotency-conflict-b-" + walletB.String()
	opening := moneyMust("100.00", "BRL")
	if err := service.OpenWallet(ctx, walletA, playerA, opening, now); err != nil {
		t.Fatal(err)
	}
	if err := service.OpenWallet(ctx, walletB, playerB, opening, now); err != nil {
		t.Fatal(err)
	}

	provider := "idempotency-conflict-provider-" + uuid.New().String()
	key := "idempotency-conflict-key-" + uuid.New().String()
	commandA := validCommand(walletA, uuid.New(), "idempotency-conflict-external-a-"+uuid.New().String(), wager.Bet, moneyMust("10.00", "BRL"))
	commandA.ProviderID = provider
	commandA.IdempotencyKey = key
	commandA.PlayerID = playerA
	commandB := validCommand(walletB, uuid.New(), "idempotency-conflict-external-b-"+uuid.New().String(), wager.Bet, moneyMust("10.00", "BRL"))
	commandB.ProviderID = provider
	commandB.IdempotencyKey = key
	commandB.PlayerID = playerB

	type outcome struct {
		command Command
		result  Result
		err     error
	}
	start := make(chan struct{})
	outcomes := make(chan outcome, 2)
	var waitGroup sync.WaitGroup
	for _, command := range []Command{commandA, commandB} {
		waitGroup.Add(1)
		go func(command Command) {
			defer waitGroup.Done()
			<-start
			result, err := service.Process(ctx, command, now)
			outcomes <- outcome{command: command, result: result, err: err}
		}(command)
	}
	close(start)
	waitGroup.Wait()
	close(outcomes)

	processed := 0
	conflicted := 0
	var winner outcome
	for current := range outcomes {
		if current.err == nil {
			processed++
			winner = current
			if current.result.State != wager.Processed || current.result.Balance != 9000 {
				t.Fatalf("winning result: %+v", current.result)
			}
			continue
		}
		conflicted++
		if !errors.Is(current.err, ErrIdempotencyConflict) {
			t.Fatalf("conflicting result error = %v", current.err)
		}
		var postgresErr *pgconn.PgError
		if errors.As(current.err, &postgresErr) && postgresErr.Code == "23505" {
			t.Fatalf("unique violation escaped application: %v", current.err)
		}
		if current.result != (Result{}) {
			t.Fatalf("rolled back result was returned: %+v", current.result)
		}
	}
	if processed != 1 || conflicted != 1 {
		t.Fatalf("processed=%d conflicted=%d, want one of each", processed, conflicted)
	}

	transactionRepository := postgres.NewWagerTransactionRepository(db)
	countA, err := transactionRepository.CountByExternal(ctx, provider, commandA.ExternalID)
	if err != nil {
		t.Fatal(err)
	}
	countB, err := transactionRepository.CountByExternal(ctx, provider, commandB.ExternalID)
	if err != nil {
		t.Fatal(err)
	}
	if countA+countB != 1 {
		t.Fatalf("conflicting transaction count = %d, want 1", countA+countB)
	}
	if count, err := transactionRepository.CountByIdempotency(ctx, provider, key); err != nil || count != 1 {
		t.Fatalf("conflicting idempotency record count = %d: %v", count, err)
	}
	if count, err := postgres.NewLedgerRepository(db).CountByTransaction(ctx, winner.command.ID); err != nil || count != 1 {
		t.Fatalf("winning ledger count = %d: %v", count, err)
	}
	if winner.command.ID == commandA.ID {
		if wallet, err := postgres.NewWalletRepository(db).Find(ctx, walletB); err != nil || wallet.Balance != 10000 {
			t.Fatalf("conflicted wallet A/B state: %+v %v", wallet, err)
		}
	} else {
		if wallet, err := postgres.NewWalletRepository(db).Find(ctx, walletA); err != nil || wallet.Balance != 10000 {
			t.Fatalf("conflicted wallet A/B state: %+v %v", wallet, err)
		}
	}
}
