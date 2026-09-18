package http

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/leonardodacosta/distributedBettingProcessing/internal/application/financial"
	"github.com/leonardodacosta/distributedBettingProcessing/internal/application/query"
	"github.com/leonardodacosta/distributedBettingProcessing/internal/domain/money"
	"github.com/leonardodacosta/distributedBettingProcessing/internal/domain/wager"
	"github.com/leonardodacosta/distributedBettingProcessing/internal/infrastructure/keycloak"
	"github.com/leonardodacosta/distributedBettingProcessing/internal/infrastructure/postgres"
)

func TestHTTPFinancialFlowAgainstPostgreSQL(t *testing.T) {
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

	providerID := "http-provider-alpha-" + uuid.New().String()
	playerID := "http-player-" + uuid.New().String()
	otherProviderID := "http-provider-beta-" + uuid.New().String()
	otherPlayerID := "http-player-beta-" + uuid.New().String()
	auth := &fakeAuthenticator{identities: map[string]keycloak.Identity{
		"provider-token":       testIdentity(providerID, keycloak.RoleProvider),
		"internal-token":       testIdentity("wallet-admin", keycloak.RoleInternal),
		"other-provider-token": testIdentity(otherProviderID, keycloak.RoleProvider),
	}}
	service := financial.NewService(db)
	queries := query.NewService(db)
	handler := testRouter(auth, service, queries, postgres.NewPostgresChecker(db))
	server := httptest.NewServer(handler)
	defer server.Close()

	client := server.Client()
	openBody := `{"playerId":"` + playerID + `","openingBalance":{"amount":"100.00","currency":"BRL"}}`
	openRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, server.URL+"/wallets", strings.NewReader(openBody))
	if err != nil {
		t.Fatal(err)
	}
	openRequest.Header.Set("Authorization", "Bearer internal-token")
	openResponse, err := client.Do(openRequest)
	if err != nil {
		t.Fatal(err)
	}
	openPayload, err := readResponse(openResponse, &walletResponse{})
	if err != nil {
		t.Fatal(err)
	}
	wallet := openPayload.(walletResponse)
	if openResponse.StatusCode != http.StatusCreated || wallet.Balance.Minor() != 10000 {
		t.Fatalf("wallet response = %d %+v", openResponse.StatusCode, wallet)
	}

	externalID := "http-external-" + uuid.New().String()
	idempotencyKey := "http-idem-" + uuid.New().String()
	transactionBody := `{"externalId":"` + externalID + `","walletId":"` + wallet.WalletID + `","playerId":"` + playerID + `","gameId":"game-1","roundId":"round-1","type":"BET","amount":{"amount":"25.08","currency":"BRL"}}`
	postTransactionAs := func(token, body, key string) (wageringResultResponse, *http.Response) {
		request, requestErr := http.NewRequestWithContext(ctx, http.MethodPost, server.URL+"/wagering/transactions", strings.NewReader(body))
		if requestErr != nil {
			t.Fatal(requestErr)
		}
		request.Header.Set("Authorization", "Bearer "+token)
		request.Header.Set("Idempotency-Key", key)
		response, requestErr := client.Do(request)
		if requestErr != nil {
			t.Fatal(requestErr)
		}
		payload, requestErr := readResponse(response, &wageringResultResponse{})
		if requestErr != nil {
			t.Fatal(requestErr)
		}
		return payload.(wageringResultResponse), response
	}
	postTransaction := func() (wageringResultResponse, *http.Response) {
		return postTransactionAs("provider-token", transactionBody, idempotencyKey)
	}

	firstResult, firstResponse := postTransaction()
	if firstResponse.StatusCode != http.StatusOK || firstResult.State != string(wager.Processed) || firstResult.Amount.Minor() != 2508 || firstResult.Balance == nil || firstResult.Balance.Minor() != 7492 {
		t.Fatalf("first transaction response = %d %+v", firstResponse.StatusCode, firstResult)
	}
	if firstResult.IdempotentReplay == nil || *firstResult.IdempotentReplay {
		t.Fatalf("first transaction replay flag = %v", firstResult.IdempotentReplay)
	}

	replayResult, replayResponse := postTransaction()
	if replayResponse.StatusCode != http.StatusOK || replayResult.TransactionID != firstResult.TransactionID || replayResult.Balance == nil || replayResult.Balance.Minor() != 7492 {
		t.Fatalf("replay response = %d %+v", replayResponse.StatusCode, replayResult)
	}
	if replayResult.IdempotentReplay == nil || !*replayResult.IdempotentReplay {
		t.Fatalf("replay flag = %v", replayResult.IdempotentReplay)
	}

	walletID, err := uuid.Parse(wallet.WalletID)
	if err != nil {
		t.Fatal(err)
	}
	transactionID, err := uuid.Parse(firstResult.TransactionID)
	if err != nil {
		t.Fatal(err)
	}
	persistedWallet, err := postgres.NewWalletRepository(db).Find(ctx, walletID)
	if err != nil {
		t.Fatal(err)
	}
	if persistedWallet.Balance != 7492 {
		t.Fatalf("persisted wallet balance = %d, want 7492", persistedWallet.Balance)
	}
	persistedLedger, err := postgres.NewLedgerRepository(db).CountByTransaction(ctx, transactionID)
	if err != nil {
		t.Fatal(err)
	}
	if persistedLedger != 1 {
		t.Fatalf("ledger entries for transaction = %d, want 1", persistedLedger)
	}

	getTransaction := func(token, transactionID string) *http.Response {
		request, requestErr := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/wagering/transactions/"+transactionID, nil)
		if requestErr != nil {
			t.Fatal(requestErr)
		}
		request.Header.Set("Authorization", "Bearer "+token)
		response, requestErr := client.Do(request)
		if requestErr != nil {
			t.Fatal(requestErr)
		}
		return response
	}
	providerResponse := getTransaction("provider-token", firstResult.TransactionID)
	if providerResponse.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(providerResponse.Body)
		providerResponse.Body.Close()
		t.Fatalf("provider transaction lookup = %d %s", providerResponse.StatusCode, body)
	}
	providerResponse.Body.Close()
	otherProviderResponse := getTransaction("other-provider-token", firstResult.TransactionID)
	if otherProviderResponse.StatusCode != http.StatusNotFound {
		body, _ := io.ReadAll(otherProviderResponse.Body)
		otherProviderResponse.Body.Close()
		t.Fatalf("other provider transaction lookup = %d %s", otherProviderResponse.StatusCode, body)
	}
	otherProviderResponse.Body.Close()

	// Seed a real transaction owned by beta, then ask alpha to resolve it by
	// both supported lookup identities. The SQL query must hide the row rather
	// than load it globally and filter it after the fact.
	betaWalletID := uuid.New()
	betaOpening, err := money.New("10.00", "BRL")
	if err != nil {
		t.Fatal(err)
	}
	if err := service.OpenWallet(ctx, betaWalletID, otherPlayerID, betaOpening, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	betaExternalID := "http-beta-external-" + uuid.New().String()
	betaBody := `{"externalId":"` + betaExternalID + `","walletId":"` + betaWalletID.String() + `","playerId":"` + otherPlayerID + `","gameId":"game-beta","roundId":"round-beta","type":"BET","amount":{"amount":"1.00","currency":"BRL"}}`
	betaResult, betaResponse := postTransactionAs("other-provider-token", betaBody, "http-beta-key-"+uuid.New().String())
	if betaResponse.StatusCode != http.StatusOK || betaResult.State != string(wager.Processed) {
		t.Fatalf("beta transaction response = %d %+v", betaResponse.StatusCode, betaResult)
	}
	betaTransactionResponse := getTransaction("provider-token", betaResult.TransactionID)
	betaTransactionBody, _ := io.ReadAll(betaTransactionResponse.Body)
	betaTransactionResponse.Body.Close()
	if betaTransactionResponse.StatusCode != http.StatusNotFound || strings.Contains(string(betaTransactionBody), betaExternalID) || strings.Contains(string(betaTransactionBody), betaResult.TransactionID) {
		t.Fatalf("alpha beta transaction-id lookup = %d %s", betaTransactionResponse.StatusCode, betaTransactionBody)
	}
	alphaExternalRequest, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/providers/"+providerID+"/wagering/transactions/"+betaExternalID, nil)
	if err != nil {
		t.Fatal(err)
	}
	alphaExternalRequest.Header.Set("Authorization", "Bearer provider-token")
	alphaExternalResponse, err := client.Do(alphaExternalRequest)
	if err != nil {
		t.Fatal(err)
	}
	alphaExternalBody, _ := io.ReadAll(alphaExternalResponse.Body)
	alphaExternalResponse.Body.Close()
	if alphaExternalResponse.StatusCode != http.StatusNotFound || strings.Contains(string(alphaExternalBody), betaExternalID) || strings.Contains(string(alphaExternalBody), betaResult.TransactionID) {
		t.Fatalf("alpha beta external-id lookup = %d %s", alphaExternalResponse.StatusCode, alphaExternalBody)
	}

	ledgerRequest, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/wallets/"+wallet.WalletID+"/ledger?limit=1", nil)
	if err != nil {
		t.Fatal(err)
	}
	ledgerRequest.Header.Set("Authorization", "Bearer internal-token")
	ledgerHTTPResponse, err := client.Do(ledgerRequest)
	if err != nil {
		t.Fatal(err)
	}
	ledgerPayload, err := readResponse(ledgerHTTPResponse, &ledgerResponse{})
	if err != nil {
		t.Fatal(err)
	}
	ledgerPage := ledgerPayload.(ledgerResponse)
	if ledgerHTTPResponse.StatusCode != http.StatusOK || len(ledgerPage.Entries) != 1 || ledgerPage.NextCursor == nil {
		t.Fatalf("ledger response = %d %+v", ledgerHTTPResponse.StatusCode, ledgerPage)
	}

	readyResponse, err := client.Get(server.URL + "/health/ready")
	if err != nil {
		t.Fatal(err)
	}
	if readyResponse.StatusCode != http.StatusOK {
		t.Fatalf("readiness status = %d", readyResponse.StatusCode)
	}
	readyResponse.Body.Close()
}

func TestHTTPRejectsZeroAmountWithFinancialValidationEnvelope(t *testing.T) {
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

	providerID := "http-zero-provider-" + uuid.New().String()
	playerID := "http-zero-player-" + uuid.New().String()
	walletID := uuid.New()
	opening, err := money.New("10.00", "BRL")
	if err != nil {
		t.Fatal(err)
	}
	if err := financial.NewService(db).OpenWallet(ctx, walletID, playerID, opening, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	handler := testRouter(
		&fakeAuthenticator{identities: map[string]keycloak.Identity{
			"provider-token": testIdentity(providerID, keycloak.RoleProvider),
		}},
		financial.NewService(db),
		query.NewService(db),
	)
	server := httptest.NewServer(handler)
	defer server.Close()

	for _, operationType := range []string{"BET", "WIN"} {
		body := `{"externalId":"zero-` + strings.ToLower(operationType) + `-` + uuid.New().String() + `","walletId":"` + walletID.String() + `","playerId":"` + playerID + `","gameId":"game-zero","roundId":"round-zero","type":"` + operationType + `","amount":{"amount":"0.00","currency":"BRL"}}`
		request, err := http.NewRequestWithContext(ctx, http.MethodPost, server.URL+"/wagering/transactions", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Authorization", "Bearer provider-token")
		request.Header.Set("Idempotency-Key", "zero-"+strings.ToLower(operationType)+"-"+uuid.New().String())
		response, err := server.Client().Do(request)
		if err != nil {
			t.Fatal(err)
		}
		var envelope errorEnvelope
		if err := json.NewDecoder(response.Body).Decode(&envelope); err != nil {
			response.Body.Close()
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != http.StatusBadRequest || envelope.Error.Code != codeInvalidRequest {
			t.Fatalf("%s response = %d %+v", operationType, response.StatusCode, envelope)
		}
	}
}

func TestHTTPReplayStatesConflictsConcurrencyAndNewService(t *testing.T) {
	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		t.Skip("DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	db, err := postgres.NewRepository(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	providerID := "http-replay-provider-" + uuid.New().String()
	playerID := "http-replay-player-" + uuid.New().String()
	walletID := uuid.New()
	opening, err := money.New("100.00", "BRL")
	if err != nil {
		t.Fatal(err)
	}
	if err := financial.NewService(db).OpenWallet(ctx, walletID, playerID, opening, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	auth := &fakeAuthenticator{identities: map[string]keycloak.Identity{
		"provider-token": testIdentity(providerID, keycloak.RoleProvider),
	}}
	newServer := func() *httptest.Server {
		return httptest.NewServer(testRouter(auth, financial.NewService(db), query.NewService(db)))
	}
	post := func(server *httptest.Server, operationType, externalID, amount, key, reference string) (wageringResultResponse, int, string, error) {
		body := `{"externalId":"` + externalID + `","walletId":"` + walletID.String() + `","playerId":"` + playerID + `","gameId":"game-replay","roundId":"round-replay","type":"` + operationType + `","amount":{"amount":"` + amount + `","currency":"BRL"}`
		if reference != "" {
			body += `,"referenceExternalId":"` + reference + `"`
		}
		body += `}`
		request, err := http.NewRequestWithContext(ctx, http.MethodPost, server.URL+"/wagering/transactions", strings.NewReader(body))
		if err != nil {
			return wageringResultResponse{}, 0, "", err
		}
		request.Header.Set("Authorization", "Bearer provider-token")
		request.Header.Set("Idempotency-Key", key)
		response, err := server.Client().Do(request)
		if err != nil {
			return wageringResultResponse{}, 0, "", err
		}
		defer response.Body.Close()
		data, err := io.ReadAll(response.Body)
		if err != nil {
			return wageringResultResponse{}, response.StatusCode, "", err
		}
		if response.StatusCode != http.StatusOK {
			return wageringResultResponse{}, response.StatusCode, string(data), nil
		}
		var result wageringResultResponse
		if err := json.Unmarshal(data, &result); err != nil {
			return wageringResultResponse{}, response.StatusCode, string(data), err
		}
		return result, response.StatusCode, string(data), nil
	}
	assertSameSnapshot := func(name string, first, replay wageringResultResponse) {
		t.Helper()
		if replay.TransactionID != first.TransactionID || replay.State != first.State || replay.Amount.Amount() != first.Amount.Amount() || replay.Amount.Currency() != first.Amount.Currency() {
			t.Fatalf("%s replay snapshot = transaction=%s/%s state=%s/%s amount=%s %s/%s %s", name, replay.TransactionID, first.TransactionID, replay.State, first.State, replay.Amount.Amount(), replay.Amount.Currency(), first.Amount.Amount(), first.Amount.Currency())
		}
		if replay.Balance == nil || first.Balance == nil || replay.Balance.Amount() != first.Balance.Amount() || replay.Balance.Currency() != first.Balance.Currency() {
			t.Fatalf("%s replay balance snapshot = %+v, first = %+v", name, replay.Balance, first.Balance)
		}
	}

	processedExternal := "http-replay-processed-" + uuid.New().String()
	processedKey := "http-replay-processed-key-" + uuid.New().String()
	server := newServer()
	firstProcessed, status, _, err := post(server, "BET", processedExternal, "1.00", processedKey, "")
	if err != nil || status != http.StatusOK || firstProcessed.State != string(wager.Processed) || firstProcessed.IdempotentReplay == nil || *firstProcessed.IdempotentReplay {
		t.Fatalf("first processed request = %d %+v %v", status, firstProcessed, err)
	}
	server.Close()

	server = newServer()
	replayedProcessed, status, _, err := post(server, "BET", processedExternal, "1.00", processedKey, "")
	if err != nil || status != http.StatusOK || replayedProcessed.TransactionID != firstProcessed.TransactionID || replayedProcessed.Balance == nil || firstProcessed.Balance == nil || replayedProcessed.Balance.Minor() != firstProcessed.Balance.Minor() || replayedProcessed.IdempotentReplay == nil || !*replayedProcessed.IdempotentReplay {
		t.Fatalf("processed replay through new service = %d %+v %v", status, replayedProcessed, err)
	}
	assertSameSnapshot("processed", firstProcessed, replayedProcessed)
	if count, err := postgres.NewLedgerRepository(db).CountByTransaction(ctx, uuid.MustParse(firstProcessed.TransactionID)); err != nil || count != 1 {
		t.Fatalf("processed replay ledger count = %d/%v", count, err)
	}

	rejectedExternal := "http-replay-rejected-" + uuid.New().String()
	rejectedKey := "http-replay-rejected-key-" + uuid.New().String()
	firstRejected, status, _, err := post(server, "BET", rejectedExternal, "200.00", rejectedKey, "")
	if err != nil || status != http.StatusOK || firstRejected.State != string(wager.Rejected) || firstRejected.IdempotentReplay == nil || *firstRejected.IdempotentReplay {
		t.Fatalf("first rejected request = %d %+v %v", status, firstRejected, err)
	}
	replayedRejected, status, _, err := post(server, "BET", rejectedExternal, "200.00", rejectedKey, "")
	if err != nil || status != http.StatusOK || replayedRejected.TransactionID != firstRejected.TransactionID || replayedRejected.Balance == nil || firstRejected.Balance == nil || replayedRejected.Balance.Minor() != firstRejected.Balance.Minor() || replayedRejected.IdempotentReplay == nil || !*replayedRejected.IdempotentReplay {
		t.Fatalf("rejected replay = %d %+v %v", status, replayedRejected, err)
	}
	assertSameSnapshot("rejected", firstRejected, replayedRejected)

	pendingExternal := "http-replay-pending-" + uuid.New().String()
	pendingKey := "http-replay-pending-key-" + uuid.New().String()
	firstPending, status, _, err := post(server, "REFUND", pendingExternal, "1.00", pendingKey, "")
	if err != nil || status != http.StatusOK || firstPending.State != string(wager.PendingReference) || firstPending.IdempotentReplay == nil || *firstPending.IdempotentReplay {
		t.Fatalf("first pending request = %d %+v %v", status, firstPending, err)
	}
	replayedPending, status, _, err := post(server, "REFUND", pendingExternal, "1.00", pendingKey, "")
	if err != nil || status != http.StatusOK || replayedPending.TransactionID != firstPending.TransactionID || replayedPending.Balance == nil || firstPending.Balance == nil || replayedPending.Balance.Minor() != firstPending.Balance.Minor() || replayedPending.IdempotentReplay == nil || !*replayedPending.IdempotentReplay {
		t.Fatalf("pending replay = %d %+v %v", status, replayedPending, err)
	}
	assertSameSnapshot("pending reference", firstPending, replayedPending)

	var body string
	_, status, body, err = post(server, "BET", processedExternal, "2.00", processedKey, "")
	if err != nil || status != http.StatusConflict || !strings.Contains(body, `"code":"IDEMPOTENCY_CONFLICT"`) {
		t.Fatalf("same key divergent payload = %d %s %v", status, body, err)
	}
	otherKey := "http-replay-external-conflict-key-" + uuid.New().String()
	_, status, body, err = post(server, "BET", processedExternal, "1.00", otherKey, "")
	if err != nil || status != http.StatusConflict || !strings.Contains(body, `"code":"IDEMPOTENCY_CONFLICT"`) {
		t.Fatalf("same external identity divergent key = %d %s %v", status, body, err)
	}

	processedID := uuid.MustParse(firstProcessed.TransactionID)
	if count, err := postgres.NewWagerTransactionRepository(db).CountByExternal(ctx, providerID, processedExternal); err != nil || count != 1 {
		t.Fatalf("processed transaction count = %d/%v", count, err)
	}
	if count, err := postgres.NewWagerTransactionRepository(db).CountByExternal(ctx, providerID, rejectedExternal); err != nil || count != 1 {
		t.Fatalf("rejected transaction count = %d/%v", count, err)
	}
	if count, err := postgres.NewWagerTransactionRepository(db).CountByExternal(ctx, providerID, pendingExternal); err != nil || count != 1 {
		t.Fatalf("pending transaction count = %d/%v", count, err)
	}
	if count, err := postgres.NewLedgerRepository(db).CountByTransaction(ctx, uuid.MustParse(firstRejected.TransactionID)); err != nil || count != 0 {
		t.Fatalf("rejected replay ledger count = %d/%v", count, err)
	}
	if count, err := postgres.NewLedgerRepository(db).CountByTransaction(ctx, uuid.MustParse(firstPending.TransactionID)); err != nil || count != 0 {
		t.Fatalf("pending replay ledger count = %d/%v", count, err)
	}
	if count, err := postgres.NewLedgerRepository(db).CountByTransaction(ctx, processedID); err != nil || count != 1 {
		t.Fatalf("processed ledger count after conflicts = %d/%v", count, err)
	}
	currentWallet, err := postgres.NewWalletRepository(db).Find(ctx, walletID)
	if err != nil {
		t.Fatal(err)
	}
	if currentWallet.Balance != 9900 {
		t.Fatalf("rejected/pending replay changed wallet balance = %d, want 9900", currentWallet.Balance)
	}

	concurrentWalletID := uuid.New()
	concurrentPlayerID := "http-concurrent-player-" + uuid.New().String()
	concurrentOpening, err := money.New("100.00", "BRL")
	if err != nil {
		t.Fatal(err)
	}
	if err := financial.NewService(db).OpenWallet(ctx, concurrentWalletID, concurrentPlayerID, concurrentOpening, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	concurrentExternal := "http-replay-concurrent-" + uuid.New().String()
	concurrentKey := "http-replay-concurrent-key-" + uuid.New().String()
	concurrentPost := func() (wageringResultResponse, int, error) {
		body := `{"externalId":"` + concurrentExternal + `","walletId":"` + concurrentWalletID.String() + `","playerId":"` + concurrentPlayerID + `","gameId":"game-concurrent","roundId":"round-concurrent","type":"BET","amount":{"amount":"80.00","currency":"BRL"}}`
		request, err := http.NewRequestWithContext(ctx, http.MethodPost, server.URL+"/wagering/transactions", strings.NewReader(body))
		if err != nil {
			return wageringResultResponse{}, 0, err
		}
		request.Header.Set("Authorization", "Bearer provider-token")
		request.Header.Set("Idempotency-Key", concurrentKey)
		response, err := server.Client().Do(request)
		if err != nil {
			return wageringResultResponse{}, 0, err
		}
		defer response.Body.Close()
		var result wageringResultResponse
		if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
			return wageringResultResponse{}, response.StatusCode, err
		}
		return result, response.StatusCode, nil
	}
	results := make(chan struct {
		result wageringResultResponse
		status int
		err    error
	}, 2)
	for i := 0; i < 2; i++ {
		go func() {
			result, status, err := concurrentPost()
			results <- struct {
				result wageringResultResponse
				status int
				err    error
			}{result: result, status: status, err: err}
		}()
	}
	var concurrentResults []wageringResultResponse
	replayCount := 0
	for i := 0; i < 2; i++ {
		outcome := <-results
		if outcome.err != nil || outcome.status != http.StatusOK || outcome.result.IdempotentReplay == nil {
			t.Fatalf("concurrent HTTP outcome = %d %+v %v", outcome.status, outcome.result, outcome.err)
		}
		if *outcome.result.IdempotentReplay {
			replayCount++
		}
		concurrentResults = append(concurrentResults, outcome.result)
	}
	if replayCount != 1 || concurrentResults[0].TransactionID != concurrentResults[1].TransactionID {
		t.Fatalf("concurrent replay results = %+v, replay count = %d", concurrentResults, replayCount)
	}
	concurrentID := uuid.MustParse(concurrentResults[0].TransactionID)
	if count, err := postgres.NewWagerTransactionRepository(db).CountByIdempotency(ctx, providerID, concurrentKey); err != nil || count != 1 {
		t.Fatalf("concurrent transaction count = %d/%v", count, err)
	}
	if count, err := postgres.NewLedgerRepository(db).CountByTransaction(ctx, concurrentID); err != nil || count != 1 {
		t.Fatalf("concurrent ledger count = %d/%v", count, err)
	}
	concurrentWallet, err := postgres.NewWalletRepository(db).Find(ctx, concurrentWalletID)
	if err != nil {
		t.Fatal(err)
	}
	if concurrentWallet.Balance != 2000 {
		t.Fatalf("concurrent wallet balance = %d, want 2000", concurrentWallet.Balance)
	}
	server.Close()
}

func readResponse(response *http.Response, target any) (any, error) {
	defer response.Body.Close()
	if err := json.NewDecoder(response.Body).Decode(target); err != nil {
		return nil, err
	}
	switch value := target.(type) {
	case *walletResponse:
		return *value, nil
	case *wageringResultResponse:
		return *value, nil
	case *ledgerResponse:
		return *value, nil
	default:
		return nil, errors.New("unsupported response target")
	}
}
