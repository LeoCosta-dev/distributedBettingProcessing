package financial

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/leonardodacosta/distributedBettingProcessing/internal/domain/wager"
	"github.com/leonardodacosta/distributedBettingProcessing/internal/infrastructure/postgres"
)

func TestConcurrentBetsAcrossThreeInstances(t *testing.T) {
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		t.Skip("DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	instances := make([]*postgres.Repository, 0, 3)
	services := make([]*Service, 0, 3)
	for range 3 {
		db, err := postgres.NewRepository(ctx, url)
		if err != nil {
			t.Fatal(err)
		}
		instances = append(instances, db)
		services = append(services, NewService(db))
	}
	defer func() {
		for _, db := range instances {
			db.Close()
		}
	}()

	now := time.Now().UTC()
	walletID := uuid.New()
	playerID := "concurrency-player-" + walletID.String()
	if err := services[0].OpenWallet(ctx, walletID, playerID, moneyMust("100.00", "BRL"), now); err != nil {
		t.Fatal(err)
	}
	independentWalletID := uuid.New()
	independentPlayerID := "concurrency-player-" + independentWalletID.String()
	if err := services[0].OpenWallet(ctx, independentWalletID, independentPlayerID, moneyMust("100.00", "BRL"), now); err != nil {
		t.Fatal(err)
	}

	firstID, secondID, independentID := uuid.New(), uuid.New(), uuid.New()
	first := validCommand(walletID, firstID, "concurrent-bet-"+firstID.String(), wager.Bet, moneyMust("80.00", "BRL"))
	second := validCommand(walletID, secondID, "concurrent-bet-"+secondID.String(), wager.Bet, moneyMust("80.00", "BRL"))
	independent := validCommand(independentWalletID, independentID, "independent-bet-"+independentID.String(), wager.Bet, moneyMust("10.00", "BRL"))
	first.PlayerID, second.PlayerID, independent.PlayerID = playerID, playerID, independentPlayerID

	type outcome struct {
		result Result
		err    error
	}
	start := make(chan struct{})
	outcomes := make(chan outcome, 3)
	var group sync.WaitGroup
	for i, command := range []Command{first, second, independent} {
		group.Add(1)
		go func(service *Service, command Command) {
			defer group.Done()
			<-start
			result, err := service.Process(ctx, command, now)
			outcomes <- outcome{result: result, err: err}
		}(services[i], command)
	}
	close(start)
	group.Wait()
	close(outcomes)

	processed, rejected := 0, 0
	for current := range outcomes {
		if current.err != nil {
			t.Fatalf("concurrent operation failed: %v", current.err)
		}
		switch current.result.State {
		case wager.Processed:
			processed++
		case wager.Rejected:
			rejected++
		default:
			t.Fatalf("unexpected concurrent state: %+v", current.result)
		}
	}
	if processed != 2 || rejected != 1 {
		t.Fatalf("processed=%d rejected=%d, want two processed and one rejected", processed, rejected)
	}

	wallet, err := postgres.NewWalletRepository(instances[2]).Find(ctx, walletID)
	if err != nil {
		t.Fatal(err)
	}
	if wallet.Balance != 2000 || wallet.Version != 2 {
		t.Fatalf("same-wallet state = balance %d version %d, want 2000 and 2", wallet.Balance, wallet.Version)
	}
	ledgerCount, err := postgres.NewLedgerRepository(instances[2]).Count(ctx, walletID)
	if err != nil {
		t.Fatal(err)
	}
	if ledgerCount != 2 {
		t.Fatalf("same-wallet ledger entries = %d, want opening plus one debit", ledgerCount)
	}
	for _, command := range []Command{first, second} {
		record, err := postgres.NewWagerTransactionRepository(instances[2]).FindByExternal(ctx, command.ProviderID, command.ExternalID)
		if err != nil {
			t.Fatal(err)
		}
		if record.State != string(wager.Processed) && record.State != string(wager.Rejected) {
			t.Fatalf("unexpected persisted state: %s", record.State)
		}
	}

	independentWallet, err := postgres.NewWalletRepository(instances[2]).Find(ctx, independentWalletID)
	if err != nil {
		t.Fatal(err)
	}
	if independentWallet.Balance != 9000 || independentWallet.Version != 2 {
		t.Fatalf("independent wallet state = balance %d version %d, want 9000 and 2", independentWallet.Balance, independentWallet.Version)
	}
	if count, err := postgres.NewLedgerRepository(instances[2]).Count(ctx, independentWalletID); err != nil || count != 2 {
		t.Fatalf("independent wallet ledger entries = %d: %v", count, err)
	}
}

func TestIndependentWalletsDoNotShareWalletLock(t *testing.T) {
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		t.Skip("DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	lockerDB, err := postgres.NewRepository(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer lockerDB.Close()
	workerDB, err := postgres.NewRepository(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer workerDB.Close()
	service := NewService(lockerDB)
	walletA, walletB := uuid.New(), uuid.New()
	now := time.Now().UTC()
	if err := service.OpenWallet(ctx, walletA, "parallel-player-"+walletA.String(), moneyMust("100.00", "BRL"), now); err != nil {
		t.Fatal(err)
	}
	if err := service.OpenWallet(ctx, walletB, "parallel-player-"+walletB.String(), moneyMust("100.00", "BRL"), now); err != nil {
		t.Fatal(err)
	}

	locked := make(chan struct{})
	release := make(chan struct{})
	lockResult := make(chan error, 1)
	go func() {
		lockResult <- lockerDB.WithTx(ctx, func(ctx context.Context, tx *postgres.Repository) error {
			if _, err := postgres.NewWalletRepository(tx).FindForUpdate(ctx, walletA); err != nil {
				return err
			}
			close(locked)
			<-release
			return nil
		})
	}()
	<-locked

	operationDone := make(chan error, 1)
	go func() {
		_, err := NewService(workerDB).Process(ctx, validCommand(walletB, uuid.New(), "parallel-bet-"+walletB.String(), wager.Bet, moneyMust("10.00", "BRL")), now)
		operationDone <- err
	}()
	select {
	case err := <-operationDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		close(release)
		t.Fatal("operation on an independent wallet was blocked by wallet A")
	}
	close(release)
	if err := <-lockResult; err != nil {
		t.Fatal(err)
	}
}

func TestConcurrentBetsRejectWithoutNegativeBalance(t *testing.T) {
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		t.Skip("DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	firstDB, err := postgres.NewRepository(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer firstDB.Close()
	secondDB, err := postgres.NewRepository(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer secondDB.Close()
	serviceA, serviceB := NewService(firstDB), NewService(secondDB)
	walletID := uuid.New()
	if err := serviceA.OpenWallet(ctx, walletID, "two-bets-player-"+walletID.String(), moneyMust("100.00", "BRL"), time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	commands := []Command{
		validCommand(walletID, uuid.New(), "two-bets-a-"+walletID.String(), wager.Bet, moneyMust("80.00", "BRL")),
		validCommand(walletID, uuid.New(), "two-bets-b-"+walletID.String(), wager.Bet, moneyMust("80.00", "BRL")),
	}
	commands[0].PlayerID, commands[1].PlayerID = "two-bets-player-"+walletID.String(), "two-bets-player-"+walletID.String()
	start := make(chan struct{})
	results := make(chan struct {
		result Result
		err    error
	}, 2)
	var group sync.WaitGroup
	for i, command := range commands {
		group.Add(1)
		go func(service *Service, command Command) {
			defer group.Done()
			<-start
			result, err := service.Process(ctx, command, time.Now().UTC())
			results <- struct {
				result Result
				err    error
			}{result, err}
		}(map[int]*Service{0: serviceA, 1: serviceB}[i], command)
	}
	close(start)
	group.Wait()
	close(results)
	processed, rejected := 0, 0
	for current := range results {
		if current.err != nil {
			t.Fatal(current.err)
		}
		switch current.result.State {
		case wager.Processed:
			processed++
		case wager.Rejected:
			rejected++
		}
	}
	if processed != 1 || rejected != 1 {
		t.Fatalf("processed=%d rejected=%d, want one each", processed, rejected)
	}
	wallet, err := postgres.NewWalletRepository(firstDB).Find(ctx, walletID)
	if err != nil {
		t.Fatal(err)
	}
	if wallet.Balance != 2000 {
		t.Fatalf("final balance = %d, want 2000", wallet.Balance)
	}
	if wallet.Balance < 0 {
		t.Fatal("wallet balance became negative")
	}
	if count, err := postgres.NewLedgerRepository(firstDB).Count(ctx, walletID); err != nil || count != 2 {
		t.Fatalf("ledger entries = %d: %v", count, err)
	}
}

const (
	concurrencyWorkerEnv      = "DISTRIBUTED_BETTING_CONCURRENCY_WORKER"
	concurrencyStartEnv       = "DISTRIBUTED_BETTING_CONCURRENCY_START"
	concurrencyCommandEnv     = "DISTRIBUTED_BETTING_CONCURRENCY_COMMAND"
	concurrencyWorkerIndexEnv = "DISTRIBUTED_BETTING_CONCURRENCY_WORKER_INDEX"
	concurrencyWorkerCountEnv = "DISTRIBUTED_BETTING_CONCURRENCY_WORKER_COUNT"
)

type concurrencyWorkerOutput struct {
	Result Result `json:"result"`
	Error  string `json:"error,omitempty"`
}

func TestConcurrentBetsAcrossThreeOSProcesses(t *testing.T) {
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		t.Skip("DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	setupDB, err := postgres.NewRepository(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer setupDB.Close()
	service := NewService(setupDB)
	now := time.Now().UTC()
	walletID := uuid.New()
	playerID := "process-concurrency-player-" + walletID.String()
	if err := service.OpenWallet(ctx, walletID, playerID, moneyMust("100.00", "BRL"), now); err != nil {
		t.Fatal(err)
	}

	commands := make([]Command, 3)
	for i := range commands {
		id := uuid.New()
		commands[i] = validCommand(walletID, id, fmt.Sprintf("process-concurrent-bet-%d-%s", i, id), wager.Bet, moneyMust("80.00", "BRL"))
		commands[i].PlayerID = playerID
	}

	releasePath := t.TempDir() + "/release"
	ready := make(chan struct {
		index int
		pid   int
	}, len(commands))
	done := make(chan struct {
		index   int
		output  concurrencyWorkerOutput
		stderr  string
		waitErr error
	}, len(commands))

	for index, command := range commands {
		payload, err := json.Marshal(command)
		if err != nil {
			t.Fatal(err)
		}
		worker := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestConcurrencyWorkerProcess$", "-test.v")
		worker.Env = append(os.Environ(),
			concurrencyWorkerEnv+"=1",
			concurrencyStartEnv+"="+releasePath,
			concurrencyCommandEnv+"="+string(payload),
			concurrencyWorkerIndexEnv+"="+strconv.Itoa(index),
			concurrencyWorkerCountEnv+"="+strconv.Itoa(len(commands)),
			"DATABASE_URL="+url,
		)
		stdout, err := worker.StdoutPipe()
		if err != nil {
			t.Fatal(err)
		}
		stderr, err := worker.StderrPipe()
		if err != nil {
			t.Fatal(err)
		}
		if err := worker.Start(); err != nil {
			t.Fatal(err)
		}
		go func(index int, worker *exec.Cmd, stdout io.ReadCloser, stderr io.ReadCloser) {
			var output concurrencyWorkerOutput
			var stderrBuffer bytes.Buffer
			stderrDone := make(chan struct{})
			go func() {
				_, _ = io.Copy(&stderrBuffer, stderr)
				close(stderrDone)
			}()

			scanner := bufio.NewScanner(stdout)
			for scanner.Scan() {
				line := scanner.Text()
				if fields := strings.Fields(line); len(fields) == 2 && fields[0] == "READY" {
					pid, parseErr := strconv.Atoi(fields[1])
					if parseErr == nil {
						ready <- struct {
							index int
							pid   int
						}{index: index, pid: pid}
					}
				}
				if strings.HasPrefix(line, "RESULT ") {
					_ = json.Unmarshal([]byte(strings.TrimPrefix(line, "RESULT ")), &output)
				}
			}
			waitErr := worker.Wait()
			<-stderrDone
			done <- struct {
				index   int
				output  concurrencyWorkerOutput
				stderr  string
				waitErr error
			}{index: index, output: output, stderr: stderrBuffer.String(), waitErr: waitErr}
		}(index, worker, stdout, stderr)
	}

	pids := make(map[int]struct{}, len(commands))
	for range commands {
		select {
		case signal := <-ready:
			pids[signal.pid] = struct{}{}
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	if len(pids) != len(commands) {
		t.Fatalf("worker processes were not independent: pids=%v", pids)
	}
	if err := os.WriteFile(releasePath, []byte("release"), 0600); err != nil {
		t.Fatal(err)
	}

	outputs := make(map[int]concurrencyWorkerOutput, len(commands))
	for range commands {
		select {
		case result := <-done:
			if result.waitErr != nil {
				t.Fatalf("worker %d failed: %v; stderr=%s", result.index, result.waitErr, result.stderr)
			}
			if result.output.Error != "" {
				t.Fatalf("worker %d returned error: %s", result.index, result.output.Error)
			}
			outputs[result.index] = result.output
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}

	transactionRepository := postgres.NewWagerTransactionRepository(setupDB)
	ledgerRepository := postgres.NewLedgerRepository(setupDB)
	processed, rejected := 0, 0
	for index, command := range commands {
		workerResult, ok := outputs[index]
		if !ok {
			t.Fatalf("missing result from worker %d", index)
		}
		if workerResult.Result.Balance < 0 || workerResult.Result.TransactionID != command.ID {
			t.Fatalf("invalid worker result %d: %+v", index, workerResult.Result)
		}
		record, err := transactionRepository.FindByID(ctx, command.ID)
		if err != nil {
			t.Fatal(err)
		}
		if workerResult.Result.State != wager.State(record.State) {
			t.Fatalf("worker %d result state %s differs from persisted state %s", index, workerResult.Result.State, record.State)
		}
		ledgerCount, err := ledgerRepository.CountByTransaction(ctx, command.ID)
		if err != nil {
			t.Fatal(err)
		}
		switch record.State {
		case string(wager.Processed):
			processed++
			if ledgerCount != 1 {
				t.Fatalf("processed worker %d ledger entries = %d, want 1", index, ledgerCount)
			}
		case string(wager.Rejected):
			rejected++
			if ledgerCount != 0 {
				t.Fatalf("rejected worker %d ledger entries = %d, want 0", index, ledgerCount)
			}
		default:
			t.Fatalf("worker %d persisted unexpected state %s", index, record.State)
		}
	}
	if processed != 1 || rejected != 2 {
		t.Fatalf("processed=%d rejected=%d, want one processed and two rejected", processed, rejected)
	}
	wallet, err := postgres.NewWalletRepository(setupDB).Find(ctx, walletID)
	if err != nil {
		t.Fatal(err)
	}
	if wallet.Balance != 2000 || wallet.Version != 2 || wallet.Balance < 0 {
		t.Fatalf("final wallet state = balance %d version %d, want 2000 and 2", wallet.Balance, wallet.Version)
	}
	if ledgerCount, err := ledgerRepository.Count(ctx, walletID); err != nil || ledgerCount != 2 {
		t.Fatalf("total ledger entries = %d, want opening plus one debit: %v", ledgerCount, err)
	}
}

func TestConcurrencyWorkerProcess(t *testing.T) {
	if os.Getenv(concurrencyWorkerEnv) != "1" {
		return
	}
	url := os.Getenv("DATABASE_URL")
	startPath := os.Getenv(concurrencyStartEnv)
	commandPayload := os.Getenv(concurrencyCommandEnv)
	workerIndex, indexErr := strconv.Atoi(os.Getenv(concurrencyWorkerIndexEnv))
	workerCount, countErr := strconv.Atoi(os.Getenv(concurrencyWorkerCountEnv))
	if url == "" || startPath == "" || commandPayload == "" || indexErr != nil || countErr != nil || workerIndex < 0 || workerIndex >= workerCount || workerCount < 1 {
		t.Fatal("worker environment is incomplete")
	}
	var command Command
	if err := json.Unmarshal([]byte(commandPayload), &command); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(startPath); err == nil {
		t.Fatal("worker release barrier already exists")
	} else if !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if _, err := fmt.Fprintf(os.Stdout, "READY %d\n", os.Getpid()); err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(startPath); err == nil {
			break
		} else if !os.IsNotExist(err) {
			t.Fatal(err)
		}
		time.Sleep(5 * time.Millisecond)
	}
	armedPath := fmt.Sprintf("%s.armed.%d", startPath, workerIndex)
	if err := os.WriteFile(armedPath, []byte("armed"), 0600); err != nil {
		t.Fatal(err)
	}
	for index := 0; index < workerCount; index++ {
		path := fmt.Sprintf("%s.armed.%d", startPath, index)
		for {
			if _, err := os.Stat(path); err == nil {
				break
			} else if !os.IsNotExist(err) {
				t.Fatal(err)
			}
			time.Sleep(5 * time.Millisecond)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	db, err := postgres.NewRepository(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	result, err := NewService(db).Process(ctx, command, time.Now().UTC())
	output := concurrencyWorkerOutput{Result: result}
	if err != nil {
		output.Error = err.Error()
	}
	encoded, marshalErr := json.Marshal(output)
	if marshalErr != nil {
		t.Fatal(marshalErr)
	}
	if _, writeErr := fmt.Fprintf(os.Stdout, "RESULT %s\n", encoded); writeErr != nil {
		t.Fatal(writeErr)
	}
	if err != nil {
		t.Fatal(err)
	}
}
