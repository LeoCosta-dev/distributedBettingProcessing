package financial

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/leonardodacosta/distributedBettingProcessing/internal/domain/wager"
	"github.com/leonardodacosta/distributedBettingProcessing/internal/infrastructure/postgres"
	"github.com/leonardodacosta/distributedBettingProcessing/internal/observability"
)

func TestProcessMessageAtomicallyCompletesInboxAndReplaysWithoutMutation(t *testing.T) {
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

	walletID := uuid.New()
	playerID := "inbox-player-" + walletID.String()
	now := time.Now().UTC()
	metrics := observability.NewMetrics()
	service := NewService(db, metrics)
	if err := service.OpenWallet(ctx, walletID, playerID, moneyMust("100.00", "BRL"), now); err != nil {
		t.Fatal(err)
	}
	command := validCommand(walletID, uuid.New(), "inbox-bet-"+walletID.String(), wager.Bet, moneyMust("25.00", "BRL"))
	command.PlayerID = playerID
	command.PayloadHash = "caller-value-is-replaced"
	hash, err := CanonicalPayloadHash(command)
	if err != nil {
		t.Fatal(err)
	}
	const consumerName = "wager-consumer"
	messageID := "message-" + walletID.String()
	first, duplicate, err := service.ProcessMessage(ctx, consumerName, messageID, hash, command, now)
	if err != nil || duplicate || first.State != wager.Processed || first.Balance != 7500 {
		t.Fatalf("first message: result=%+v duplicate=%t error=%v", first, duplicate, err)
	}

	inbox := postgres.NewInboxRepository(db)
	record, err := inbox.Find(ctx, consumerName, messageID)
	if err != nil || record.CompletedAt == nil || record.PayloadHash != hash {
		t.Fatalf("inbox record: %+v error=%v", record, err)
	}
	transactions := postgres.NewWagerTransactionRepository(db)
	if count, countErr := transactions.CountByExternal(ctx, command.ProviderID, command.ExternalID); countErr != nil || count != 1 {
		t.Fatalf("first delivery transactions = %d, error=%v", count, countErr)
	}
	ledger := postgres.NewLedgerRepository(db)
	if count, countErr := ledger.CountByTransaction(ctx, first.TransactionID); countErr != nil || count != 1 {
		t.Fatalf("first delivery ledger entries = %d, error=%v", count, countErr)
	}
	outbox := postgres.NewOutboxRepository(db)
	if count, countErr := outbox.CountByTypeAndAggregate(ctx, "WagerTransactionProcessed", first.TransactionID); countErr != nil || count != 1 {
		t.Fatalf("first delivery processed events = %d, error=%v", count, countErr)
	}
	if count, countErr := outbox.CountByTypeAndCausation(ctx, "WalletBalanceChanged", first.TransactionID.String()); countErr != nil || count != 1 {
		t.Fatalf("first delivery balance events = %d, error=%v", count, countErr)
	}
	if wallet, walletErr := postgres.NewWalletRepository(db).Find(ctx, walletID); walletErr != nil || wallet.Balance != 7500 {
		t.Fatalf("first delivery wallet = %+v, error=%v", wallet, walletErr)
	}

	replayCommand := command
	replayCommand.ID = uuid.New()
	replay, duplicate, err := service.ProcessMessage(ctx, consumerName, messageID, hash, replayCommand, now.Add(time.Second))
	if err != nil || !duplicate || replay != first {
		t.Fatalf("replayed message: result=%+v duplicate=%t error=%v", replay, duplicate, err)
	}
	if count, countErr := ledger.CountByTransaction(ctx, first.TransactionID); countErr != nil || count != 1 {
		t.Fatalf("same hash replay ledger entries = %d, error=%v", count, countErr)
	}

	changed := replayCommand
	changed.ID = uuid.New()
	changed.Amount = moneyMust("26.00", "BRL")
	changedHash, err := CanonicalPayloadHash(changed)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := service.ProcessMessage(ctx, consumerName, messageID, changedHash, changed, now.Add(2*time.Second)); !errors.Is(err, ErrInboxPayloadMismatch) {
		t.Fatalf("persisted inbox hash mismatch error = %v", err)
	}
	if record, findErr := inbox.Find(ctx, consumerName, messageID); findErr != nil || record.PayloadHash != hash || record.CompletedAt == nil {
		t.Fatalf("hash mismatch changed inbox record: %+v error=%v", record, findErr)
	}
	if count, countErr := transactions.CountByExternal(ctx, changed.ProviderID, changed.ExternalID); countErr != nil || count != 1 {
		t.Fatalf("hash mismatch transaction count = %d, error=%v", count, countErr)
	}
	if count, countErr := ledger.CountByTransaction(ctx, first.TransactionID); countErr != nil || count != 1 {
		t.Fatalf("hash mismatch ledger entries = %d, error=%v", count, countErr)
	}

	// A separate pool/service models a process restart. The only state it sees
	// is PostgreSQL, so a replay cannot depend on consumer memory.
	otherDB, err := postgres.NewRepository(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer otherDB.Close()
	restartCommand := command
	restartCommand.ID = uuid.New()
	restarted, duplicate, err := NewService(otherDB).ProcessMessage(ctx, consumerName, messageID, hash, restartCommand, now.Add(3*time.Second))
	if err != nil || !duplicate || restarted != first {
		t.Fatalf("restart replay: result=%+v duplicate=%t error=%v", restarted, duplicate, err)
	}
	if count, countErr := ledger.CountByTransaction(ctx, first.TransactionID); countErr != nil || count != 1 {
		t.Fatalf("restart replay ledger entries = %d, error=%v", count, countErr)
	}
	if got := metrics.Render(); !strings.Contains(got, `wager_duplicate_total{kind="sqs_inbox"} 1`) {
		t.Fatalf("SQS inbox duplicate was not observed in metrics: %s", got)
	}
}

func TestProcessMessageConcurrentDuplicateAcrossInstances(t *testing.T) {
	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		t.Skip("DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	dbA, err := postgres.NewRepository(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer dbA.Close()
	dbB, err := postgres.NewRepository(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer dbB.Close()
	serviceA, serviceB := NewService(dbA), NewService(dbB)
	now := time.Now().UTC()
	walletID := uuid.New()
	playerID := "inbox-concurrent-player-" + walletID.String()
	if err := serviceA.OpenWallet(ctx, walletID, playerID, moneyMust("100.00", "BRL"), now); err != nil {
		t.Fatal(err)
	}
	command := validCommand(walletID, uuid.New(), "inbox-concurrent-"+walletID.String(), wager.Bet, moneyMust("25.00", "BRL"))
	command.PlayerID = playerID
	hash, err := CanonicalPayloadHash(command)
	if err != nil {
		t.Fatal(err)
	}
	const consumerName = "wager-consumer"
	messageID := "concurrent-message-" + walletID.String()

	type outcome struct {
		result    Result
		duplicate bool
		err       error
	}
	start := make(chan struct{})
	outcomes := make(chan outcome, 2)
	var group sync.WaitGroup
	for _, service := range []*Service{serviceA, serviceB} {
		group.Add(1)
		go func(service *Service) {
			defer group.Done()
			<-start
			candidate := command
			candidate.ID = uuid.New()
			result, duplicate, processErr := service.ProcessMessage(ctx, consumerName, messageID, hash, candidate, now)
			outcomes <- outcome{result: result, duplicate: duplicate, err: processErr}
		}(service)
	}
	close(start)
	group.Wait()
	close(outcomes)

	duplicates := 0
	var first Result
	for current := range outcomes {
		if current.err != nil {
			t.Fatalf("concurrent ProcessMessage: %v", current.err)
		}
		if current.result.State != wager.Processed || current.result.Balance != 7500 {
			t.Fatalf("concurrent result: %+v", current.result)
		}
		if first == (Result{}) {
			first = current.result
		} else if current.result != first {
			t.Fatalf("concurrent replay differs: %+v versus %+v", current.result, first)
		}
		if current.duplicate {
			duplicates++
		}
	}
	if duplicates != 1 {
		t.Fatalf("duplicate outcomes = %d, want 1", duplicates)
	}
	if count, countErr := postgres.NewWagerTransactionRepository(dbA).CountByExternal(ctx, command.ProviderID, command.ExternalID); countErr != nil || count != 1 {
		t.Fatalf("concurrent transactions = %d, error=%v", count, countErr)
	}
	if count, countErr := postgres.NewLedgerRepository(dbA).CountByTransaction(ctx, first.TransactionID); countErr != nil || count != 1 {
		t.Fatalf("concurrent ledger entries = %d, error=%v", count, countErr)
	}
	if record, findErr := postgres.NewInboxRepository(dbA).Find(ctx, consumerName, messageID); findErr != nil || record.CompletedAt == nil || record.PayloadHash != hash {
		t.Fatalf("concurrent inbox record: %+v error=%v", record, findErr)
	}
	if wallet, walletErr := postgres.NewWalletRepository(dbA).Find(ctx, walletID); walletErr != nil || wallet.Balance != 7500 {
		t.Fatalf("concurrent wallet = %+v, error=%v", wallet, walletErr)
	}
}

func TestProcessMessageIdentityConflictRollsBackInboxAndFinancialState(t *testing.T) {
	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		t.Skip("DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	dbA, err := postgres.NewRepository(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer dbA.Close()
	dbB, err := postgres.NewRepository(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer dbB.Close()
	serviceA, serviceB := NewService(dbA), NewService(dbB)
	now := time.Now().UTC()
	walletA, walletB := uuid.New(), uuid.New()
	playerA := "inbox-conflict-player-a-" + walletA.String()
	playerB := "inbox-conflict-player-b-" + walletB.String()
	if err := serviceA.OpenWallet(ctx, walletA, playerA, moneyMust("100.00", "BRL"), now); err != nil {
		t.Fatal(err)
	}
	if err := serviceA.OpenWallet(ctx, walletB, playerB, moneyMust("100.00", "BRL"), now); err != nil {
		t.Fatal(err)
	}
	provider := "inbox-conflict-provider-" + uuid.New().String()
	key := "inbox-conflict-key-" + uuid.New().String()
	commandA := validCommand(walletA, uuid.New(), "inbox-conflict-external-a-"+uuid.New().String(), wager.Bet, moneyMust("10.00", "BRL"))
	commandA.ProviderID, commandA.IdempotencyKey, commandA.PlayerID = provider, key, playerA
	commandB := validCommand(walletB, uuid.New(), "inbox-conflict-external-b-"+uuid.New().String(), wager.Bet, moneyMust("10.00", "BRL"))
	commandB.ProviderID, commandB.IdempotencyKey, commandB.PlayerID = provider, key, playerB
	hashA, err := CanonicalPayloadHash(commandA)
	if err != nil {
		t.Fatal(err)
	}
	hashB, err := CanonicalPayloadHash(commandB)
	if err != nil {
		t.Fatal(err)
	}

	type candidate struct {
		command Command
		hash    string
		message string
		service *Service
	}
	type outcome struct {
		candidate candidate
		result    Result
		duplicate bool
		err       error
	}
	candidates := []candidate{
		{command: commandA, hash: hashA, message: "inbox-conflict-message-a-" + uuid.New().String(), service: serviceA},
		{command: commandB, hash: hashB, message: "inbox-conflict-message-b-" + uuid.New().String(), service: serviceB},
	}
	start := make(chan struct{})
	outcomes := make(chan outcome, len(candidates))
	var group sync.WaitGroup
	for _, current := range candidates {
		group.Add(1)
		go func(current candidate) {
			defer group.Done()
			<-start
			result, duplicate, processErr := current.service.ProcessMessage(ctx, "wager-consumer", current.message, current.hash, current.command, now)
			outcomes <- outcome{candidate: current, result: result, duplicate: duplicate, err: processErr}
		}(current)
	}
	close(start)
	group.Wait()
	close(outcomes)

	var winner, loser outcome
	processed, conflicted := 0, 0
	for current := range outcomes {
		if current.err == nil {
			processed++
			winner = current
			if current.duplicate || current.result.State != wager.Processed || current.result.Balance != 9000 {
				t.Fatalf("winning ProcessMessage result: %+v duplicate=%t", current.result, current.duplicate)
			}
			continue
		}
		conflicted++
		loser = current
		if !errors.Is(current.err, ErrIdempotencyConflict) || current.result != (Result{}) || current.duplicate {
			t.Fatalf("conflicting ProcessMessage result=%+v duplicate=%t error=%v", current.result, current.duplicate, current.err)
		}
	}
	if processed != 1 || conflicted != 1 {
		t.Fatalf("processed=%d conflicted=%d, want one of each", processed, conflicted)
	}

	transactions := postgres.NewWagerTransactionRepository(dbA)
	if count, countErr := transactions.CountByIdempotency(ctx, provider, key); countErr != nil || count != 1 {
		t.Fatalf("conflicting idempotency records = %d, error=%v", count, countErr)
	}
	if count, countErr := transactions.CountByExternal(ctx, loser.candidate.command.ProviderID, loser.candidate.command.ExternalID); countErr != nil || count != 0 {
		t.Fatalf("losing financial transaction = %d, error=%v", count, countErr)
	}
	if count, countErr := postgres.NewLedgerRepository(dbA).CountByTransaction(ctx, winner.result.TransactionID); countErr != nil || count != 1 {
		t.Fatalf("winning ledger entries = %d, error=%v", count, countErr)
	}
	outbox := postgres.NewOutboxRepository(dbA)
	if count, countErr := outbox.CountByTypeAndAggregate(ctx, "WagerTransactionProcessed", winner.result.TransactionID); countErr != nil || count != 1 {
		t.Fatalf("winning processed events = %d, error=%v", count, countErr)
	}
	if count, countErr := outbox.CountByTypeAndCausation(ctx, "WalletBalanceChanged", winner.result.TransactionID.String()); countErr != nil || count != 1 {
		t.Fatalf("winning balance events = %d, error=%v", count, countErr)
	}
	if count, countErr := postgres.NewLedgerRepository(dbA).CountByTransaction(ctx, loser.candidate.command.ID); countErr != nil || count != 0 {
		t.Fatalf("losing ledger entries = %d, error=%v", count, countErr)
	}
	inbox := postgres.NewInboxRepository(dbA)
	if record, findErr := inbox.Find(ctx, "wager-consumer", winner.candidate.message); findErr != nil || record.CompletedAt == nil {
		t.Fatalf("winner inbox record: %+v error=%v", record, findErr)
	}
	if _, findErr := inbox.Find(ctx, "wager-consumer", loser.candidate.message); !errors.Is(findErr, pgx.ErrNoRows) {
		t.Fatalf("loser inbox survived rollback: %v", findErr)
	}
	if wallet, walletErr := postgres.NewWalletRepository(dbA).Find(ctx, loser.candidate.command.WalletID); walletErr != nil || wallet.Balance != 10000 {
		t.Fatalf("losing wallet state = %+v, error=%v", wallet, walletErr)
	}

	// Retrying the losing SQS delivery cannot create a financial result or an
	// inbox completion in a separate transaction.
	result, duplicate, retryErr := loser.candidate.service.ProcessMessage(ctx, "wager-consumer", loser.candidate.message, loser.candidate.hash, loser.candidate.command, now.Add(time.Second))
	if !errors.Is(retryErr, ErrIdempotencyConflict) || result != (Result{}) || duplicate {
		t.Fatalf("retry conflict result=%+v duplicate=%t error=%v", result, duplicate, retryErr)
	}
	if _, findErr := inbox.Find(ctx, "wager-consumer", loser.candidate.message); !errors.Is(findErr, pgx.ErrNoRows) {
		t.Fatalf("retry created a loser inbox record: %v", findErr)
	}
}

func TestProcessMessageRollsBackInboxWhenFinancialTransactionDoesNotCommit(t *testing.T) {
	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		t.Skip("DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	db, err := postgres.NewRepository(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	command := validCommand(uuid.New(), uuid.New(), "inbox-rollback", wager.Bet, moneyMust("1.00", "BRL"))
	hash, err := CanonicalPayloadHash(command)
	if err != nil {
		t.Fatal(err)
	}
	service := NewService(db)
	_, _, err = service.ProcessMessage(ctx, "wager-consumer", "rollback-message", hash, command, time.Now().UTC())
	if err == nil {
		t.Fatal("missing wallet unexpectedly processed")
	}
	if _, findErr := postgres.NewInboxRepository(db).Find(ctx, "wager-consumer", "rollback-message"); !errors.Is(findErr, pgx.ErrNoRows) {
		t.Fatalf("rolled-back inbox record: %v", findErr)
	}
}
