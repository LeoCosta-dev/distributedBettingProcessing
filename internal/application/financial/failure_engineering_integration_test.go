package financial

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/leonardodacosta/distributedBettingProcessing/internal/domain/money"
	"github.com/leonardodacosta/distributedBettingProcessing/internal/domain/wager"
	"github.com/leonardodacosta/distributedBettingProcessing/internal/infrastructure/postgres"
)

// TestLoop11DatabaseFailureBeforeCommitRollsBackEveryFinancialWrite injects a
// PostgreSQL error on the final outbox write. By then the transaction has
// already attempted its wallet, transaction and ledger writes, so a passing
// test proves the SQL boundary rather than a pre-validation shortcut.
func TestLoop11DatabaseFailureBeforeCommitRollsBackEveryFinancialWrite(t *testing.T) {
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
	service := NewService(db)
	walletID := uuid.New()
	playerID := "loop11-rollback-player-" + walletID.String()
	opening, err := money.New("100.00", "BRL")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err := service.OpenWallet(ctx, walletID, playerID, opening, now); err != nil {
		t.Fatal(err)
	}
	amount, err := money.New("25.00", "BRL")
	if err != nil {
		t.Fatal(err)
	}
	commandID := uuid.New()
	cmd := validCommand(walletID, commandID, "loop11-rollback-"+commandID.String(), wager.Bet, amount)
	cmd.PlayerID = playerID
	triggerName := "loop11_fail_outbox_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	functionName := triggerName + "_fn"
	admin, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	correlationLiteral := strings.ReplaceAll(cmd.IdempotencyKey, "'", "''")
	if _, err := admin.Exec(ctx, fmt.Sprintf(`CREATE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.correlation_id = '%s' THEN RAISE EXCEPTION 'loop11 injected pre-commit database failure'; END IF; RETURN NEW; END; $$`, functionName, correlationLiteral)); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, fmt.Sprintf(`CREATE TRIGGER %s BEFORE INSERT ON outbox FOR EACH ROW EXECUTE FUNCTION %s()`, triggerName, functionName)); err != nil {
		_, _ = admin.Exec(ctx, "DROP FUNCTION "+functionName+"()")
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(context.Background(), "DROP TRIGGER IF EXISTS "+triggerName+" ON outbox")
		_, _ = admin.Exec(context.Background(), "DROP FUNCTION IF EXISTS "+functionName+"()")
	})
	if _, err := service.Process(ctx, cmd, now.Add(time.Second)); err == nil {
		t.Fatal("injected pre-commit database failure unexpectedly succeeded")
	}
	walletRecord, err := postgres.NewWalletRepository(db).Find(ctx, walletID)
	if err != nil || walletRecord.Balance != opening.Minor() || walletRecord.Version != 1 {
		t.Fatalf("wallet after rolled-back processing = %+v, error=%v", walletRecord, err)
	}
	if _, err := postgres.NewWagerTransactionRepository(db).FindByExternal(ctx, cmd.ProviderID, cmd.ExternalID); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("rolled-back transaction is visible: %v", err)
	}
	if count, err := postgres.NewLedgerRepository(db).Count(ctx, walletID); err != nil || count != 1 {
		t.Fatalf("ledger after rollback = %d, error=%v", count, err)
	}
	if count, err := postgres.NewOutboxRepository(db).CountByTypeAndAggregate(ctx, "WagerTransactionProcessed", commandID); err != nil || count != 0 {
		t.Fatalf("outbox after rollback = %d, error=%v", count, err)
	}
	// Removing only the injected database fault models dependency recovery. The
	// same durable command can now be attempted normally; the prior failed
	// transaction left no half-visible identity or financial mutation behind.
	if _, err := admin.Exec(ctx, "DROP TRIGGER "+triggerName+" ON outbox"); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, "DROP FUNCTION "+functionName+"()"); err != nil {
		t.Fatal(err)
	}
	result, err := service.Process(ctx, cmd, now.Add(2*time.Second))
	if err != nil || result.State != wager.Processed || result.Balance != 7500 {
		t.Fatalf("processing after database recovery = %+v, error=%v", result, err)
	}
	if count, err := postgres.NewLedgerRepository(db).CountByTransaction(ctx, commandID); err != nil || count != 1 {
		t.Fatalf("ledger after recovered processing = %d, error=%v", count, err)
	}
}

func TestLoop11ProcessCrashBeforeCommitHelper(t *testing.T) {
	if os.Getenv("LOOP11_PRECOMMIT_CRASH_HELPER") != "1" {
		return
	}
	walletID, err := uuid.Parse(os.Getenv("LOOP11_WALLET_ID"))
	if err != nil {
		t.Fatal(err)
	}
	commandID, err := uuid.Parse(os.Getenv("LOOP11_COMMAND_ID"))
	if err != nil {
		t.Fatal(err)
	}
	amount, err := money.New("25.00", "BRL")
	if err != nil {
		t.Fatal(err)
	}
	db, err := postgres.NewRepository(context.Background(), os.Getenv("DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	cmd := validCommand(walletID, commandID, os.Getenv("LOOP11_EXTERNAL_ID"), wager.Bet, amount)
	cmd.PlayerID = os.Getenv("LOOP11_PLAYER_ID")
	cmd.IdempotencyKey = os.Getenv("LOOP11_IDEMPOTENCY_KEY")
	_, _ = NewService(db).Process(context.Background(), cmd, time.Now().UTC())
	t.Fatal("pre-commit crash helper returned before parent terminated it")
}

// TestLoop11ProcessCrashBeforeCommitRollsBackTransaction kills a separate Go
// process while its PostgreSQL transaction is held in a test-only trigger.
// The advisory lock is the synchronization point: it is held only after the
// application has reached the final outbox write and before that transaction
// can commit. No production hook or timing sleep coordinates the crash.
func TestLoop11ProcessCrashBeforeCommitRollsBackTransaction(t *testing.T) {
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
	walletID := uuid.New()
	playerID := "loop11-precommit-crash-player-" + walletID.String()
	opening, err := money.New("100.00", "BRL")
	if err != nil {
		t.Fatal(err)
	}
	if err := service.OpenWallet(ctx, walletID, playerID, opening, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	commandID := uuid.New()
	externalID := "loop11-precommit-crash-" + commandID.String()
	idempotencyKey := "idem-" + commandID.String()
	triggerName := "loop11_crash_outbox_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	functionName := triggerName + "_fn"
	lockKey := time.Now().UnixNano()
	admin, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	if _, err := admin.Exec(ctx, fmt.Sprintf(`CREATE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.correlation_id = '%s' THEN PERFORM pg_advisory_xact_lock(%d); PERFORM pg_sleep(30); END IF; RETURN NEW; END; $$`, functionName, idempotencyKey, lockKey)); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, fmt.Sprintf(`CREATE TRIGGER %s BEFORE INSERT ON outbox FOR EACH ROW EXECUTE FUNCTION %s()`, triggerName, functionName)); err != nil {
		_, _ = admin.Exec(ctx, "DROP FUNCTION "+functionName+"()")
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(context.Background(), "DROP TRIGGER IF EXISTS "+triggerName+" ON outbox")
		_, _ = admin.Exec(context.Background(), "DROP FUNCTION IF EXISTS "+functionName+"()")
	})
	child := exec.Command(os.Args[0], "-test.run=^TestLoop11ProcessCrashBeforeCommitHelper$")
	child.Env = append(loop11EnvironmentWithoutDatabaseURL(),
		"DATABASE_URL="+databaseURL,
		"LOOP11_PRECOMMIT_CRASH_HELPER=1",
		"LOOP11_WALLET_ID="+walletID.String(),
		"LOOP11_PLAYER_ID="+playerID,
		"LOOP11_COMMAND_ID="+commandID.String(),
		"LOOP11_EXTERNAL_ID="+externalID,
		"LOOP11_IDEMPOTENCY_KEY="+idempotencyKey,
	)
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if child.Process != nil {
			_ = child.Process.Kill()
			_, _ = child.Process.Wait()
		}
	})
	if err := waitForLoop11AdvisoryLock(ctx, admin, lockKey); err != nil {
		t.Fatalf("child did not reach pre-commit transaction boundary: %v", err)
	}
	if err := child.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if err := child.Wait(); err == nil {
		t.Fatal("pre-commit crash helper unexpectedly exited cleanly")
	}
	child.Process = nil
	if _, err := postgres.NewWagerTransactionRepository(db).FindByExternal(ctx, "provider", externalID); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("crashed pre-commit transaction is visible: %v", err)
	}
	walletRecord, err := postgres.NewWalletRepository(db).Find(ctx, walletID)
	if err != nil || walletRecord.Balance != opening.Minor() || walletRecord.Version != 1 {
		t.Fatalf("wallet after pre-commit process crash = %+v, error=%v", walletRecord, err)
	}
	if count, err := postgres.NewLedgerRepository(db).Count(ctx, walletID); err != nil || count != 1 {
		t.Fatalf("ledger after pre-commit process crash = %d, error=%v", count, err)
	}
	if count, err := postgres.NewOutboxRepository(db).CountByTypeAndAggregate(ctx, "WagerTransactionProcessed", commandID); err != nil || count != 0 {
		t.Fatalf("outbox after pre-commit process crash = %d, error=%v", count, err)
	}
}

func loop11EnvironmentWithoutDatabaseURL() []string {
	env := os.Environ()
	filtered := make([]string, 0, len(env))
	for _, entry := range env {
		if !strings.HasPrefix(entry, "DATABASE_URL=") {
			filtered = append(filtered, entry)
		}
	}
	return filtered
}

func waitForLoop11AdvisoryLock(ctx context.Context, admin *pgxpool.Pool, lockKey int64) error {
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		var acquired bool
		if err := admin.QueryRow(ctx, "SELECT CASE WHEN pg_try_advisory_lock($1) THEN pg_advisory_unlock($1) ELSE false END", lockKey).Scan(&acquired); err != nil {
			return err
		}
		if !acquired {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}
