package financial

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/leonardodacosta/distributedBettingProcessing/internal/domain/wager"
	"github.com/leonardodacosta/distributedBettingProcessing/internal/infrastructure/postgres"
)

func TestMoneyPrecisionFinancialRegression(t *testing.T) {
	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		t.Skip("DATABASE_URL is not set")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	db, err := postgres.NewRepository(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	queryPool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer queryPool.Close()
	if err := queryPool.Ping(ctx); err != nil {
		t.Fatal(err)
	}

	t.Run("BET 25.08", func(t *testing.T) {
		walletID := uuid.New()
		now := time.Now().UTC()
		if err := NewService(db).OpenWallet(ctx, walletID, "financial-player-"+walletID.String(), moneyMust("100.00", "BRL"), now); err != nil {
			t.Fatal(err)
		}

		transactionID := uuid.New()
		amount := moneyMust("25.08", "BRL")
		command := validCommand(walletID, transactionID, "money-regression-bet-"+transactionID.String(), wager.Bet, amount)
		result, err := NewService(db).Process(ctx, command, now)
		if err != nil {
			t.Fatal(err)
		}
		if result.State != wager.Processed || result.Amount.Minor() != 2508 || result.Balance != 7492 {
			t.Fatalf("result = %+v, want processed amount 2508 and balance 7492", result)
		}

		assertPersistedMoneyMovement(t, ctx, queryPool, command, "DEBIT", 2508, 10000, 7492)
	})

	t.Run("wallet 1.07 plus WIN 0.01", func(t *testing.T) {
		walletID := uuid.New()
		now := time.Now().UTC()
		if err := NewService(db).OpenWallet(ctx, walletID, "financial-player-"+walletID.String(), moneyMust("1.07", "BRL"), now); err != nil {
			t.Fatal(err)
		}

		transactionID := uuid.New()
		amount := moneyMust("0.01", "BRL")
		command := validCommand(walletID, transactionID, "money-regression-win-"+transactionID.String(), wager.Win, amount)
		result, err := NewService(db).Process(ctx, command, now)
		if err != nil {
			t.Fatal(err)
		}
		if result.State != wager.Processed || result.Amount.Minor() != 1 || result.Balance != 108 {
			t.Fatalf("result = %+v, want processed amount 1 and balance 108", result)
		}

		assertPersistedMoneyMovement(t, ctx, queryPool, command, "CREDIT", 1, 107, 108)
	})
}

func assertPersistedMoneyMovement(t *testing.T, ctx context.Context, queryPool *pgxpool.Pool, command Command, direction string, value, before, after int64) {
	t.Helper()
	beforeMoney, err := moneyFromMinor(before, "BRL")
	if err != nil {
		t.Fatal(err)
	}
	afterMoney, err := moneyFromMinor(after, "BRL")
	if err != nil {
		t.Fatal(err)
	}

	var transactionAmount int64
	var transactionState string
	if err := queryPool.QueryRow(ctx, `SELECT amount,state FROM wager_transactions WHERE id=$1`, command.ID).Scan(&transactionAmount, &transactionState); err != nil {
		t.Fatal(err)
	}
	if transactionAmount != value || transactionState != string(wager.Processed) {
		t.Fatalf("transaction amount/state = %d/%s, want %d/PROCESSED", transactionAmount, transactionState, value)
	}

	var walletBalance int64
	if err := queryPool.QueryRow(ctx, `SELECT balance FROM wallets WHERE id=$1`, command.WalletID).Scan(&walletBalance); err != nil {
		t.Fatal(err)
	}
	if walletBalance != after {
		t.Fatalf("wallet balance = %d, want %d", walletBalance, after)
	}

	var ledgerDirection, ledgerCurrency string
	var ledgerValue, ledgerBefore, ledgerAfter int64
	if err := queryPool.QueryRow(ctx, `SELECT direction,value,currency,balance_before,balance_after FROM wallet_ledger_entries WHERE transaction_id=$1`, command.ID).Scan(&ledgerDirection, &ledgerValue, &ledgerCurrency, &ledgerBefore, &ledgerAfter); err != nil {
		t.Fatal(err)
	}
	if ledgerDirection != direction || ledgerValue != value || ledgerCurrency != "BRL" || ledgerBefore != before || ledgerAfter != after {
		t.Fatalf("ledger = %s/%d/%s/%d/%d, want %s/%d/BRL/%d/%d", ledgerDirection, ledgerValue, ledgerCurrency, ledgerBefore, ledgerAfter, direction, value, before, after)
	}

	for _, eventType := range []string{"WagerTransactionProcessed", "WalletBalanceChanged"} {
		var data []byte
		if err := queryPool.QueryRow(ctx, `SELECT data FROM outbox WHERE event_type=$1 AND causation_id=$2`, eventType, command.ID.String()).Scan(&data); err != nil {
			t.Fatalf("%s event: %v", eventType, err)
		}
		var event struct {
			Amount struct {
				Amount   string `json:"amount"`
				Currency string `json:"currency"`
			} `json:"amount"`
			Money struct {
				Amount   string `json:"amount"`
				Currency string `json:"currency"`
			} `json:"money"`
			Direction     string `json:"direction"`
			BalanceBefore struct {
				Amount string `json:"amount"`
			} `json:"balanceBefore"`
			BalanceAfter struct {
				Amount string `json:"amount"`
			} `json:"balanceAfter"`
			WalletVersion int64 `json:"walletVersion"`
		}
		if err := json.Unmarshal(data, &event); err != nil {
			t.Fatalf("%s event data: %v", eventType, err)
		}
		if eventType == "WagerTransactionProcessed" && (event.Amount.Amount != command.Amount.Amount() || event.Amount.Currency != "BRL") {
			t.Fatalf("%s money = %+v, want %s BRL", eventType, event.Amount, command.Amount.Amount())
		}
		if eventType == "WalletBalanceChanged" && (event.Money.Amount != command.Amount.Amount() || event.Money.Currency != "BRL" || event.Direction != direction || event.BalanceBefore.Amount != beforeMoney.Amount() || event.BalanceAfter.Amount != afterMoney.Amount() || event.WalletVersion < 1) {
			t.Fatalf("wallet event snapshot = %+v, want direction=%s before=%d after=%d", event, direction, before, after)
		}
	}
}
