package financial

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/leonardodacosta/distributedBettingProcessing/internal/domain/money"
	"github.com/leonardodacosta/distributedBettingProcessing/internal/domain/wager"
	"github.com/leonardodacosta/distributedBettingProcessing/internal/infrastructure/postgres"
)

func TestProcessFinancialOperationsAtomically(t *testing.T) {
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		t.Skip("DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	db, err := postgres.NewRepository(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	walletID := uuid.New()
	now := time.Now().UTC()
	opening, _ := money.New("100.00", "BRL")
	if err := NewService(db).OpenWallet(ctx, walletID, "financial-player-"+walletID.String(), opening, now); err != nil {
		t.Fatal(err)
	}
	if count, err := postgres.NewOutboxRepository(db).CountByTypeAndAggregate(ctx, "WalletBalanceChanged", walletID); err != nil || count != 1 {
		t.Fatalf("opening event: %d %v", count, err)
	}
	amount, _ := money.New("80.00", "BRL")
	service := NewService(db)
	for _, tc := range []struct {
		name string
		set  func(*Command)
	}{
		{"player", func(cmd *Command) { cmd.PlayerID = "legacy" }},
		{"round", func(cmd *Command) { cmd.RoundID = "legacy" }},
		{"game", func(cmd *Command) { cmd.GameID = "legacy" }},
	} {
		t.Run("reserved "+tc.name, func(t *testing.T) {
			id := uuid.New()
			cmd := validCommand(walletID, id, "legacy-"+tc.name+"-"+id.String(), wager.Bet, amount)
			tc.set(&cmd)
			if _, err := service.Process(ctx, cmd, now); !errors.Is(err, ErrInvalidCommand) {
				t.Fatalf("error = %v, want ErrInvalidCommand", err)
			}
			if _, err := postgres.NewWagerTransactionRepository(db).FindByExternal(ctx, cmd.ProviderID, cmd.ExternalID); !errors.Is(err, pgx.ErrNoRows) {
				t.Fatalf("reserved identity was persisted: %v", err)
			}
		})
	}
	historicalID := uuid.New()
	if err := postgres.NewWagerTransactionRepository(db).Insert(ctx, postgres.WagerTransactionRecord{
		ID: historicalID, ExternalID: "historical-legacy-" + historicalID.String(), ProviderID: "provider", WalletID: walletID,
		PlayerID: "legacy", GameID: "legacy", RoundID: "legacy", Type: string(wager.Bet), Amount: 100,
		Currency: "BRL", State: string(wager.Processed), IdempotencyKey: "historical-idem-" + historicalID.String(),
		PayloadHash: "historical-hash-" + historicalID.String(), CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	historicalRefundID := uuid.New()
	historicalRefund := validCommand(walletID, historicalRefundID, "historical-refund-"+historicalRefundID.String(), wager.Refund, moneyMust("1.00", "BRL"))
	historicalRefund.ReferenceExternalID = "historical-legacy-" + historicalID.String()
	if got, err := service.Process(ctx, historicalRefund, now); err != nil || got.State != wager.Rejected {
		t.Fatalf("historical reference: %+v %v", got, err)
	}
	firstID := uuid.New()
	got, err := service.Process(ctx, validCommand(walletID, firstID, "bet-1-"+firstID.String(), wager.Bet, amount), now)
	if err != nil || got.State != wager.Processed || got.Balance != 2000 {
		t.Fatalf("processed: %+v %v", got, err)
	}
	secondID := uuid.New()
	got, err = service.Process(ctx, validCommand(walletID, secondID, "bet-2-"+secondID.String(), wager.Bet, amount), now)
	if err != nil || got.State != wager.Rejected || got.Balance != 2000 {
		t.Fatalf("rejected: %+v %v", got, err)
	}
	w, err := postgres.NewWalletRepository(db).Find(ctx, walletID)
	if err != nil || w.Balance != 2000 {
		t.Fatalf("wallet: %+v %v", w, err)
	}
	n, err := postgres.NewLedgerRepository(db).Count(ctx, walletID)
	if err != nil || n != 2 {
		t.Fatalf("ledger: %d %v", n, err)
	}
	winID := uuid.New()
	win, err := money.New("10.00", "BRL")
	if err != nil {
		t.Fatal(err)
	}
	if got, err := service.Process(ctx, validCommand(walletID, winID, "win-"+winID.String(), wager.Win, win), now); err != nil || got.State != wager.Processed || got.Balance != 3000 {
		t.Fatalf("win: %+v %v", got, err)
	}
	loss, _ := money.Zero("BRL")
	lossID := uuid.New()
	if got, err := service.Process(ctx, validCommand(walletID, lossID, "loss-"+lossID.String(), wager.Loss, loss), now); err != nil || got.State != wager.Processed || got.Balance != 3000 {
		t.Fatalf("loss: %+v %v", got, err)
	}
	refundID := uuid.New()
	refund := validCommand(walletID, refundID, "refund-"+refundID.String(), wager.Refund, amount)
	refund.ReferenceExternalID = "bet-1-" + firstID.String()
	if got, err := service.Process(ctx, refund, now); err != nil || got.State != wager.Processed || got.Balance != 11000 {
		t.Fatalf("refund: %+v %v", got, err)
	}
	duplicateRefundID := uuid.New()
	duplicateRefund := validCommand(walletID, duplicateRefundID, "refund-duplicate-"+duplicateRefundID.String(), wager.Refund, amount)
	duplicateRefund.ReferenceExternalID = "bet-1-" + firstID.String()
	if _, err := service.Process(ctx, duplicateRefund, now); err == nil {
		t.Fatal("duplicate refund was accepted")
	}
	invalidRefundID := uuid.New()
	invalidRefund := validCommand(walletID, invalidRefundID, "refund-invalid-"+invalidRefundID.String(), wager.Refund, win)
	invalidRefund.ReferenceExternalID = "bet-1-" + firstID.String()
	if got, err := service.Process(ctx, invalidRefund, now); err != nil || got.State != wager.Rejected {
		t.Fatalf("invalid reference: %+v %v", got, err)
	}
	invalidGameID := uuid.New()
	invalidGame := validCommand(walletID, invalidGameID, "refund-game-mismatch-"+invalidGameID.String(), wager.Refund, amount)
	invalidGame.GameID = "different-game"
	invalidGame.ReferenceExternalID = "bet-1-" + firstID.String()
	if got, err := service.Process(ctx, invalidGame, now); err != nil || got.State != wager.Rejected {
		t.Fatalf("game mismatch: %+v %v", got, err)
	}
	if record, err := postgres.NewWagerTransactionRepository(db).FindByExternal(ctx, "provider", invalidGame.ExternalID); err != nil || record.State != string(wager.Rejected) {
		t.Fatalf("invalid reference was not persisted as rejected: %+v %v", record, err)
	}
	rollbackID := uuid.New()
	rollback := validCommand(walletID, rollbackID, "rollback-"+rollbackID.String(), wager.Rollback, win)
	rollback.ReferenceExternalID = "win-" + winID.String()
	if got, err := service.Process(ctx, rollback, now); err != nil || got.State != wager.Processed || got.Balance != 10000 {
		t.Fatalf("rollback: %+v %v", got, err)
	}
	duplicateRollbackID := uuid.New()
	duplicateRollback := validCommand(walletID, duplicateRollbackID, "rollback-duplicate-"+duplicateRollbackID.String(), wager.Rollback, win)
	duplicateRollback.ReferenceExternalID = "win-" + winID.String()
	if _, err := service.Process(ctx, duplicateRollback, now); err == nil {
		t.Fatal("duplicate rollback was accepted")
	}
	missingID := uuid.New()
	missing := validCommand(walletID, missingID, "refund-missing-"+missingID.String(), wager.Refund, amount)
	missing.ReferenceExternalID = "does-not-exist-" + missingID.String()
	got, err = service.Process(ctx, missing, now)
	if err != nil || got.State != wager.PendingReference {
		t.Fatalf("missing reference: %+v %v", got, err)
	}
	reconciliation, err := service.Reconcile(ctx, walletID)
	if err != nil || !reconciliation.Consistent || reconciliation.WalletBalance != 10000 || reconciliation.LedgerBalance != 10000 {
		t.Fatalf("reconciliation: %+v %v", reconciliation, err)
	}
	if n, err := postgres.NewLedgerRepository(db).Count(ctx, walletID); err != nil || n != 5 {
		t.Fatalf("ledger movement count: %d %v", n, err)
	}
	if count, err := postgres.NewOutboxRepository(db).CountByTypeAndAggregate(ctx, "WalletBalanceChanged", walletID); err != nil || count != 5 {
		t.Fatalf("loss emitted balance event: %d %v", count, err)
	}
	zeroID := uuid.New()
	zero, _ := money.Zero("BRL")
	if err := service.OpenWallet(ctx, zeroID, "zero-player-"+zeroID.String(), zero, now); err != nil {
		t.Fatal(err)
	}
	if zeroWallet, err := postgres.NewWalletRepository(db).Find(ctx, zeroID); err != nil || zeroWallet.Balance != 0 {
		t.Fatalf("zero wallet: %+v %v", zeroWallet, err)
	}
	if _, err := postgres.NewWagerTransactionRepository(db).FindByExternal(ctx, "internal", "opening-"+zeroID.String()); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("zero opening transaction: %v", err)
	}

	rollbackWalletID := uuid.New()
	if err := service.OpenWallet(ctx, rollbackWalletID, "financial-player-"+rollbackWalletID.String(), opening, now); err != nil {
		t.Fatal(err)
	}
	rollbackBetID := uuid.New()
	rollbackAmount, _ := money.New("40.00", "BRL")
	rollbackBet := validCommand(rollbackWalletID, rollbackBetID, "rollback-bet-"+rollbackBetID.String(), wager.Bet, rollbackAmount)
	if got, err := service.Process(ctx, rollbackBet, now); err != nil || got.Balance != 6000 {
		t.Fatalf("rollback bet: %+v %v", got, err)
	}
	rollbackRefundID := uuid.New()
	rollbackRefund := validCommand(rollbackWalletID, rollbackRefundID, "rollback-refund-"+rollbackRefundID.String(), wager.Refund, rollbackAmount)
	rollbackRefund.ReferenceExternalID = rollbackBet.ExternalID
	if got, err := service.Process(ctx, rollbackRefund, now); err != nil || got.Balance != 10000 {
		t.Fatalf("rollback refund source: %+v %v", got, err)
	}
	rollbackOfRefundID := uuid.New()
	rollbackOfRefund := validCommand(rollbackWalletID, rollbackOfRefundID, "rollback-of-refund-"+rollbackOfRefundID.String(), wager.Rollback, rollbackAmount)
	rollbackOfRefund.ReferenceExternalID = rollbackRefund.ExternalID
	if got, err := service.Process(ctx, rollbackOfRefund, now); err != nil || got.State != wager.Processed || got.Balance != 6000 {
		t.Fatalf("rollback refund: %+v %v", got, err)
	}

	negativeWalletID := uuid.New()
	if err := service.OpenWallet(ctx, negativeWalletID, "financial-player-"+negativeWalletID.String(), opening, now); err != nil {
		t.Fatal(err)
	}
	negativeBetID := uuid.New()
	negativeBet := validCommand(negativeWalletID, negativeBetID, "negative-bet-"+negativeBetID.String(), wager.Bet, rollbackAmount)
	if _, err := service.Process(ctx, negativeBet, now); err != nil {
		t.Fatal(err)
	}
	negativeRefundID := uuid.New()
	negativeRefund := validCommand(negativeWalletID, negativeRefundID, "negative-refund-"+negativeRefundID.String(), wager.Refund, rollbackAmount)
	negativeRefund.ReferenceExternalID = negativeBet.ExternalID
	if _, err := service.Process(ctx, negativeRefund, now); err != nil {
		t.Fatal(err)
	}
	spendID := uuid.New()
	spend := validCommand(negativeWalletID, spendID, "negative-spend-"+spendID.String(), wager.Bet, moneyMust("95.00", "BRL"))
	if _, err := service.Process(ctx, spend, now); err != nil {
		t.Fatal(err)
	}
	rollbackNegativeID := uuid.New()
	rollbackNegative := validCommand(negativeWalletID, rollbackNegativeID, "negative-rollback-"+rollbackNegativeID.String(), wager.Rollback, rollbackAmount)
	rollbackNegative.ReferenceExternalID = negativeRefund.ExternalID
	if got, err := service.Process(ctx, rollbackNegative, now); err != nil || got.State != wager.Rejected || got.Balance != 500 {
		t.Fatalf("negative rollback: %+v %v", got, err)
	}
}

func validCommand(walletID, id uuid.UUID, external string, typ wager.Type, amount money.Money) Command {
	return Command{ID: id, WalletID: walletID, ExternalID: external, ProviderID: "provider", PlayerID: "financial-player-" + walletID.String(), GameID: "game-1", RoundID: "round-1", IdempotencyKey: "idem-" + id.String(), PayloadHash: "hash-" + id.String(), Type: typ, Amount: amount}
}

func moneyMust(amount, currency string) money.Money {
	value, err := money.New(amount, currency)
	if err != nil {
		panic(err)
	}
	return value
}
