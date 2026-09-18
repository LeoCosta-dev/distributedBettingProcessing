package query

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/leonardodacosta/distributedBettingProcessing/internal/infrastructure/postgres"
)

func TestQueryServiceUsesProviderScopeAndKeysetPagination(t *testing.T) {
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
	now := time.Now().UTC().Truncate(time.Microsecond)
	if err := postgres.NewWalletRepository(db).Insert(ctx, postgres.WalletRecord{
		ID:        walletID,
		PlayerID:  "query-player-" + walletID.String(),
		Currency:  "BRL",
		Balance:   51,
		Version:   1,
		CreatedAt: now,
		UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}

	transactions := postgres.NewWagerTransactionRepository(db)
	ledger := postgres.NewLedgerRepository(db)
	for i := 0; i < 51; i++ {
		transactionID := uuid.New()
		timestamp := now.Add(time.Duration(i+1) * time.Second)
		if err := transactions.Insert(ctx, postgres.WagerTransactionRecord{
			ID:             transactionID,
			ExternalID:     "query-ledger-" + transactionID.String(),
			ProviderID:     "query-provider",
			WalletID:       walletID,
			PlayerID:       "query-player-" + walletID.String(),
			GameID:         "query-game",
			RoundID:        "query-round",
			Type:           "BET",
			Amount:         1,
			Currency:       "BRL",
			State:          "PROCESSED",
			IdempotencyKey: "query-idem-" + transactionID.String(),
			PayloadHash:    "query-hash-" + transactionID.String(),
			CreatedAt:      timestamp,
			UpdatedAt:      timestamp,
		}); err != nil {
			t.Fatal(err)
		}
		if err := ledger.Insert(ctx, postgres.LedgerRecord{
			ID:            uuid.New(),
			WalletID:      walletID,
			TransactionID: transactionID,
			Direction:     "CREDIT",
			Value:         1,
			Currency:      "BRL",
			BalanceBefore: int64(i),
			BalanceAfter:  int64(i + 1),
			Timestamp:     timestamp,
		}); err != nil {
			t.Fatal(err)
		}
	}

	service := NewService(db)
	firstPage, err := service.Ledger(ctx, walletID, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(firstPage.Entries) != DefaultLedgerLimit || firstPage.NextCursor == "" {
		t.Fatalf("first page = %d entries, cursor=%q", len(firstPage.Entries), firstPage.NextCursor)
	}
	secondPage, err := service.Ledger(ctx, walletID, firstPage.NextCursor, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(secondPage.Entries) != 1 || secondPage.NextCursor != "" {
		t.Fatalf("second page = %d entries, cursor=%q", len(secondPage.Entries), secondPage.NextCursor)
	}
	if secondPage.Entries[0].ID == firstPage.Entries[len(firstPage.Entries)-1].ID {
		t.Fatal("keyset page repeated the cursor entry")
	}

	if _, err := service.Ledger(ctx, walletID, "not-a-cursor", "1"); !errors.Is(err, ErrInvalidCursor) {
		t.Fatalf("invalid cursor error = %v", err)
	}
	if _, err := service.Ledger(ctx, walletID, "", "101"); !errors.Is(err, ErrInvalidLimit) {
		t.Fatalf("invalid limit error = %v", err)
	}

	alphaTransactionID := uuid.New()
	betaTransactionID := uuid.New()
	sameExternalID := "same-external-id-" + walletID.String()
	for provider, transactionID := range map[string]uuid.UUID{"provider-alpha": alphaTransactionID, "provider-beta": betaTransactionID} {
		if err := transactions.Insert(ctx, postgres.WagerTransactionRecord{
			ID:             transactionID,
			ExternalID:     sameExternalID,
			ProviderID:     provider,
			WalletID:       walletID,
			PlayerID:       "query-player-" + walletID.String(),
			GameID:         "query-game",
			RoundID:        "query-round",
			Type:           "BET",
			Amount:         1,
			Currency:       "BRL",
			State:          "PROCESSED",
			IdempotencyKey: "query-scope-" + provider + "-" + walletID.String(),
			PayloadHash:    "query-scope-hash-" + provider + "-" + walletID.String(),
			CreatedAt:      now,
			UpdatedAt:      now,
		}); err != nil {
			t.Fatal(err)
		}
	}
	alpha, err := service.TransactionByExternalForProvider(ctx, "provider-alpha", sameExternalID)
	if err != nil {
		t.Fatal(err)
	}
	if alpha.ID != alphaTransactionID || alpha.ProviderID != "provider-alpha" {
		t.Fatalf("alpha transaction = %+v", alpha)
	}
	if _, err := service.TransactionForProvider(ctx, "provider-alpha", betaTransactionID); !errors.Is(err, ErrTransactionNotFound) {
		t.Fatalf("cross-provider transaction error = %v", err)
	}
	if _, err := service.TransactionByExternalForProvider(ctx, "provider-gamma", sameExternalID); !errors.Is(err, ErrTransactionNotFound) {
		t.Fatalf("unknown-provider transaction error = %v", err)
	}
}

func TestLedgerPaginationAdversarialCasesAgainstPostgreSQL(t *testing.T) {
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
	defer db.Close()

	wallets := postgres.NewWalletRepository(db)
	transactions := postgres.NewWagerTransactionRepository(db)
	ledger := postgres.NewLedgerRepository(db)
	service := NewService(db)
	insertWallet := func(walletID uuid.UUID) error {
		now := time.Now().UTC().Truncate(time.Microsecond)
		return wallets.Insert(ctx, postgres.WalletRecord{
			ID: walletID, PlayerID: "pagination-player-" + walletID.String(), Currency: "BRL",
			Balance: 0, Version: 1, CreatedAt: now, UpdatedAt: now,
		})
	}
	insertEntry := func(walletID uuid.UUID, index int, timestamp time.Time) (uuid.UUID, error) {
		transactionID := uuid.New()
		if err := transactions.Insert(ctx, postgres.WagerTransactionRecord{
			ID: transactionID, ExternalID: "pagination-external-" + transactionID.String(),
			ProviderID: "pagination-provider", WalletID: walletID,
			PlayerID: "pagination-player-" + walletID.String(), GameID: "pagination-game",
			RoundID: "pagination-round", Type: "BET", Amount: 1, Currency: "BRL",
			State: "PROCESSED", IdempotencyKey: "pagination-idem-" + transactionID.String(),
			PayloadHash: "pagination-hash-" + transactionID.String(), CreatedAt: timestamp, UpdatedAt: timestamp,
		}); err != nil {
			return uuid.Nil, err
		}
		ledgerID := uuid.New()
		if err := ledger.Insert(ctx, postgres.LedgerRecord{
			ID: ledgerID, WalletID: walletID, TransactionID: transactionID, Direction: "CREDIT",
			Value: 1, Currency: "BRL", BalanceBefore: int64(index), BalanceAfter: int64(index + 1), Timestamp: timestamp,
		}); err != nil {
			return uuid.Nil, err
		}
		return ledgerID, nil
	}

	emptyWalletID := uuid.New()
	if err := insertWallet(emptyWalletID); err != nil {
		t.Fatal(err)
	}
	emptyPage, err := service.Ledger(ctx, emptyWalletID, "", "1")
	if err != nil || len(emptyPage.Entries) != 0 || emptyPage.NextCursor != "" {
		t.Fatalf("empty ledger page = %+v/%v", emptyPage, err)
	}

	now := time.Now().UTC().Truncate(time.Microsecond)
	manyWalletID := uuid.New()
	if err := insertWallet(manyWalletID); err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 101; index++ {
		if _, err := insertEntry(manyWalletID, index, now); err != nil {
			t.Fatal(err)
		}
	}

	firstOne, err := service.Ledger(ctx, manyWalletID, "", "1")
	if err != nil || len(firstOne.Entries) != 1 || firstOne.NextCursor == "" {
		t.Fatalf("limit=1 page = %d/%q/%v", len(firstOne.Entries), firstOne.NextCursor, err)
	}
	firstHundred, err := service.Ledger(ctx, manyWalletID, "", "100")
	if err != nil || len(firstHundred.Entries) != 100 || firstHundred.NextCursor == "" {
		t.Fatalf("limit=100 first page = %d/%q/%v", len(firstHundred.Entries), firstHundred.NextCursor, err)
	}
	lastPage, err := service.Ledger(ctx, manyWalletID, firstHundred.NextCursor, "100")
	if err != nil || len(lastPage.Entries) != 1 || lastPage.NextCursor != "" {
		t.Fatalf("limit=100 last page = %d/%q/%v", len(lastPage.Entries), lastPage.NextCursor, err)
	}
	if lastPage.Entries[0].ID == firstHundred.Entries[len(firstHundred.Entries)-1].ID {
		t.Fatal("same-timestamp keyset pagination repeated its cursor entry")
	}
	seen := make(map[uuid.UUID]struct{}, len(firstHundred.Entries)+len(lastPage.Entries))
	for _, entry := range append(firstHundred.Entries, lastPage.Entries...) {
		if _, exists := seen[entry.ID]; exists {
			t.Fatalf("duplicate ledger entry across pages: %s", entry.ID)
		}
		seen[entry.ID] = struct{}{}
	}

	exactWalletID := uuid.New()
	if err := insertWallet(exactWalletID); err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 100; index++ {
		if _, err := insertEntry(exactWalletID, index, now); err != nil {
			t.Fatal(err)
		}
	}
	exactPage, err := service.Ledger(ctx, exactWalletID, "", "100")
	if err != nil || len(exactPage.Entries) != 100 || exactPage.NextCursor != "" {
		t.Fatalf("exact-limit page = %d/%q/%v", len(exactPage.Entries), exactPage.NextCursor, err)
	}

	appendWalletID := uuid.New()
	if err := insertWallet(appendWalletID); err != nil {
		t.Fatal(err)
	}
	firstID, err := insertEntry(appendWalletID, 0, now)
	if err != nil {
		t.Fatal(err)
	}
	secondID, err := insertEntry(appendWalletID, 1, now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	thirdID, err := insertEntry(appendWalletID, 2, now.Add(2*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	appendFirst, err := service.Ledger(ctx, appendWalletID, "", "1")
	if err != nil || len(appendFirst.Entries) != 1 || appendFirst.NextCursor == "" || appendFirst.Entries[0].ID != firstID {
		t.Fatalf("append first page = %d/%q/%v", len(appendFirst.Entries), appendFirst.NextCursor, err)
	}
	appendReady := make(chan struct{})
	releaseAppend := make(chan struct{})
	appendResult := make(chan struct {
		id  uuid.UUID
		err error
	}, 1)
	go func() {
		close(appendReady)
		<-releaseAppend
		id, insertErr := insertEntry(appendWalletID, 3, now.Add(3*time.Second))
		appendResult <- struct {
			id  uuid.UUID
			err error
		}{id: id, err: insertErr}
	}()
	<-appendReady

	releasePage := make(chan struct{})
	pageResult := make(chan struct {
		page LedgerPage
		err  error
	}, 1)
	pageStarted := make(chan struct{})
	go func() {
		close(pageStarted)
		<-releasePage
		page, pageErr := service.Ledger(ctx, appendWalletID, appendFirst.NextCursor, "1")
		pageResult <- struct {
			page LedgerPage
			err  error
		}{page: page, err: pageErr}
	}()
	<-pageStarted
	// Both operations are released together. The pre-existing third entry makes
	// page 2 deterministic whether PostgreSQL observes the append before or
	// after its SELECT; page 3, fetched after the append completes, must contain
	// both the old third entry and the newly appended fourth entry.
	close(releaseAppend)
	close(releasePage)
	inserted := <-appendResult
	if inserted.err != nil {
		t.Fatal(inserted.err)
	}
	pageTwoResult := <-pageResult
	if pageTwoResult.err != nil || len(pageTwoResult.page.Entries) != 1 || pageTwoResult.page.Entries[0].ID != secondID || pageTwoResult.page.NextCursor == "" {
		t.Fatalf("concurrent append page 2 = %+v/%v", pageTwoResult.page, pageTwoResult.err)
	}
	appendSecond, err := service.Ledger(ctx, appendWalletID, pageTwoResult.page.NextCursor, "100")
	if err != nil || len(appendSecond.Entries) != 2 || appendSecond.Entries[0].ID != thirdID || appendSecond.Entries[1].ID != inserted.id || appendSecond.NextCursor != "" {
		t.Fatalf("concurrent append page 3 = %+v/%v", appendSecond, err)
	}
	observed := []uuid.UUID{appendFirst.Entries[0].ID, pageTwoResult.page.Entries[0].ID, appendSecond.Entries[0].ID, appendSecond.Entries[1].ID}
	seenAppend := make(map[uuid.UUID]struct{}, len(observed))
	for _, id := range observed {
		if _, exists := seenAppend[id]; exists {
			t.Fatalf("duplicate entry across concurrent append pages: %s", id)
		}
		seenAppend[id] = struct{}{}
	}
	if len(seenAppend) != 4 {
		t.Fatalf("concurrent append observed %d unique entries, want 4", len(seenAppend))
	}

	for _, rawLimit := range []string{"0", "-1", "101", "not-a-number"} {
		if _, err := service.Ledger(ctx, manyWalletID, "", rawLimit); !errors.Is(err, ErrInvalidLimit) {
			t.Fatalf("limit %q error = %v, want ErrInvalidLimit", rawLimit, err)
		}
	}
	if _, err := service.Ledger(ctx, manyWalletID, "not-a-cursor", "1"); !errors.Is(err, ErrInvalidCursor) {
		t.Fatalf("malformed cursor error = %v, want ErrInvalidCursor", err)
	}

	// Cursor tokens are opaque keyset positions, but the approved Loop 6
	// decision does not bind them cryptographically to a wallet. Keep this
	// behavior visible without introducing an unapproved security rule.
	otherWalletID := uuid.New()
	if err := insertWallet(otherWalletID); err != nil {
		t.Fatal(err)
	}
	if _, err := insertEntry(otherWalletID, 0, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	crossWalletPage, err := service.Ledger(ctx, otherWalletID, firstHundred.NextCursor, "1")
	if err != nil || len(crossWalletPage.Entries) != 1 {
		t.Fatalf("cross-wallet cursor behavior = %+v/%v", crossWalletPage, err)
	}
	t.Log("cross-wallet cursor is accepted because Loop 6 does not approve wallet binding")
}

func TestParseLimitBounds(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want int
		err  bool
	}{
		{name: "default", raw: "", want: DefaultLedgerLimit},
		{name: "minimum", raw: "1", want: 1},
		{name: "maximum", raw: "100", want: MaxLedgerLimit},
		{name: "zero", raw: "0", err: true},
		{name: "over maximum", raw: "101", err: true},
		{name: "not numeric", raw: "abc", err: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := parseLimit(test.raw)
			if test.err {
				if !errors.Is(err, ErrInvalidLimit) {
					t.Fatalf("error = %v, want ErrInvalidLimit", err)
				}
				return
			}
			if err != nil || got != test.want {
				t.Fatalf("limit = %d/%v, want %d/nil", got, err, test.want)
			}
		})
	}
}

func TestWalletNotFoundIsClassified(t *testing.T) {
	if os.Getenv("DATABASE_URL") == "" {
		t.Skip("DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	db, err := postgres.NewRepository(ctx, os.Getenv("DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, err = NewService(db).Wallet(ctx, uuid.New())
	if !errors.Is(err, ErrWalletNotFound) && !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("wallet error = %v", err)
	}
}
