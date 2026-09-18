package http

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/leonardodacosta/distributedBettingProcessing/internal/application/financial"
	"github.com/leonardodacosta/distributedBettingProcessing/internal/application/query"
	"github.com/leonardodacosta/distributedBettingProcessing/internal/domain/money"
	"github.com/leonardodacosta/distributedBettingProcessing/internal/domain/wager"
	"github.com/leonardodacosta/distributedBettingProcessing/internal/infrastructure/keycloak"
)

type fakeAuthenticator struct {
	mu         sync.Mutex
	identities map[string]keycloak.Identity
	calls      int
}

func (f *fakeAuthenticator) Verify(_ context.Context, token string) (keycloak.Identity, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	identity, ok := f.identities[token]
	if !ok {
		return keycloak.Identity{}, keycloak.ErrInvalidToken
	}
	return identity, nil
}

type fakeFinancial struct {
	process func(context.Context, financial.Command, time.Time) (financial.Result, error)
	open    func(context.Context, uuid.UUID, string, money.Money, time.Time) error
	recon   func(context.Context, uuid.UUID) (financial.Reconciliation, error)
}

func (f *fakeFinancial) Process(ctx context.Context, command financial.Command, now time.Time) (financial.Result, error) {
	if f.process == nil {
		return financial.Result{}, errors.New("unexpected Process call")
	}
	return f.process(ctx, command, now)
}

func (f *fakeFinancial) OpenWallet(ctx context.Context, walletID uuid.UUID, playerID string, opening money.Money, now time.Time) error {
	if f.open == nil {
		return errors.New("unexpected OpenWallet call")
	}
	return f.open(ctx, walletID, playerID, opening, now)
}

func (f *fakeFinancial) Reconcile(ctx context.Context, walletID uuid.UUID) (financial.Reconciliation, error) {
	if f.recon == nil {
		return financial.Reconciliation{}, errors.New("unexpected Reconcile call")
	}
	return f.recon(ctx, walletID)
}

type fakeQueries struct {
	wallet              func(context.Context, uuid.UUID) (query.WalletView, error)
	ledger              func(context.Context, uuid.UUID, string, string) (query.LedgerPage, error)
	ledgerEntryCount    func(context.Context, uuid.UUID) (int64, error)
	transaction         func(context.Context, string, uuid.UUID) (query.TransactionView, error)
	externalTransaction func(context.Context, string, string) (query.TransactionView, error)
}

func (f *fakeQueries) Wallet(ctx context.Context, walletID uuid.UUID) (query.WalletView, error) {
	if f.wallet == nil {
		return query.WalletView{}, query.ErrWalletNotFound
	}
	return f.wallet(ctx, walletID)
}

func (f *fakeQueries) Ledger(ctx context.Context, walletID uuid.UUID, cursor, limit string) (query.LedgerPage, error) {
	if f.ledger == nil {
		return query.LedgerPage{}, nil
	}
	return f.ledger(ctx, walletID, cursor, limit)
}

func (f *fakeQueries) LedgerEntryCount(ctx context.Context, walletID uuid.UUID) (int64, error) {
	if f.ledgerEntryCount == nil {
		return 0, nil
	}
	return f.ledgerEntryCount(ctx, walletID)
}

func (f *fakeQueries) TransactionForProvider(ctx context.Context, providerID string, transactionID uuid.UUID) (query.TransactionView, error) {
	if f.transaction == nil {
		return query.TransactionView{}, query.ErrTransactionNotFound
	}
	return f.transaction(ctx, providerID, transactionID)
}

func (f *fakeQueries) TransactionByExternalForProvider(ctx context.Context, providerID, externalID string) (query.TransactionView, error) {
	if f.externalTransaction == nil {
		return query.TransactionView{}, query.ErrTransactionNotFound
	}
	return f.externalTransaction(ctx, providerID, externalID)
}

type fakeChecker struct {
	name string
	err  error
}

func (f fakeChecker) Name() string                { return f.name }
func (f fakeChecker) Check(context.Context) error { return f.err }

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func testIdentity(providerID string, roles ...string) keycloak.Identity {
	return keycloak.Identity{Subject: "subject-" + providerID, ProviderID: providerID, Roles: roles}
}

func testRouter(auth Authenticator, financialService FinancialUseCases, queries QueryUseCases, checkers ...Checker) http.Handler {
	return NewRouter(financialService, queries, NewHealthRegistry(checkers...), auth, testLogger()).Handler()
}

func TestRouterAuthenticationAuthorizationAndProviderIsolation(t *testing.T) {
	providerID := "provider-alpha"
	transactionID := uuid.New()
	var gotProvider string
	var gotTransaction uuid.UUID
	queries := &fakeQueries{
		transaction: func(_ context.Context, provider string, id uuid.UUID) (query.TransactionView, error) {
			gotProvider = provider
			gotTransaction = id
			return query.TransactionView{}, query.ErrTransactionNotFound
		},
	}
	auth := &fakeAuthenticator{identities: map[string]keycloak.Identity{
		"provider-token":            testIdentity(providerID, keycloak.RoleProvider),
		"provider-without-id-token": {Roles: []string{keycloak.RoleProvider}},
	}}
	financialService := &fakeFinancial{}
	handler := testRouter(auth, financialService, queries)

	t.Run("health is public", func(t *testing.T) {
		request := httptest.NewRequest(http.MethodGet, "/health/live", nil)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusOK || response.Body.String() != `{"status":"UP"}`+"\n" {
			t.Fatalf("response = %d %q", response.Code, response.Body.String())
		}
	})

	t.Run("missing bearer token is rejected", func(t *testing.T) {
		request := httptest.NewRequest(http.MethodGet, "/wagering/transactions/"+transactionID.String(), nil)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want %d", response.Code, http.StatusUnauthorized)
		}
		if !strings.Contains(response.Body.String(), `"code":"UNAUTHENTICATED"`) {
			t.Fatalf("body = %s", response.Body.String())
		}
	})

	t.Run("provider identity scopes transaction query", func(t *testing.T) {
		request := httptest.NewRequest(http.MethodGet, "/wagering/transactions/"+transactionID.String(), nil)
		request.Header.Set("Authorization", "Bearer provider-token")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want %d", response.Code, http.StatusNotFound)
		}
		if gotProvider != providerID || gotTransaction != transactionID {
			t.Fatalf("query identity = %q/%s, want %q/%s", gotProvider, gotTransaction, providerID, transactionID)
		}
	})

	t.Run("provider role without provider identity is rejected", func(t *testing.T) {
		request := httptest.NewRequest(http.MethodGet, "/wagering/transactions/"+transactionID.String(), nil)
		request.Header.Set("Authorization", "Bearer provider-without-id-token")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want %d", response.Code, http.StatusUnauthorized)
		}
	})

	t.Run("path provider mismatch is rejected before query", func(t *testing.T) {
		gotProvider = ""
		request := httptest.NewRequest(http.MethodGet, "/providers/provider-beta/wagering/transactions/external-1", nil)
		request.Header.Set("Authorization", "Bearer provider-token")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusForbidden || gotProvider != "" {
			t.Fatalf("response = %d, query provider = %q", response.Code, gotProvider)
		}
	})

	t.Run("provider cannot access internal wallet route", func(t *testing.T) {
		request := httptest.NewRequest(http.MethodGet, "/wallets/"+uuid.New().String(), nil)
		request.Header.Set("Authorization", "Bearer provider-token")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want %d", response.Code, http.StatusForbidden)
		}
	})
}

func TestProcessTransactionTranslatesAuthenticatedIdentityAndReplay(t *testing.T) {
	var captured financial.Command
	financialService := &fakeFinancial{
		process: func(_ context.Context, command financial.Command, _ time.Time) (financial.Result, error) {
			captured = command
			return financial.Result{
				TransactionID: command.ID,
				State:         wager.Processed,
				Balance:       7492,
				Amount:        command.Amount,
			}, nil
		},
	}
	auth := &fakeAuthenticator{identities: map[string]keycloak.Identity{
		"provider-token": testIdentity("provider-alpha", keycloak.RoleProvider),
	}}
	handler := testRouter(auth, financialService, &fakeQueries{})
	body := `{"providerId":"provider-alpha","externalTransactionId":"external-1","walletId":"00000000-0000-0000-0000-000000000001","playerId":"player-1","gameId":"game-1","roundId":"round-1","kind":"BET","money":{"amount":"25.08","currency":"BRL"}}`
	request := httptest.NewRequest(http.MethodPost, "/wagering/transactions", strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer provider-token")
	request.Header.Set("Idempotency-Key", "idem-1")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	if captured.ProviderID != "provider-alpha" || captured.IdempotencyKey != "idem-1" || captured.Amount.Minor() != 2508 {
		t.Fatalf("command = %+v", captured)
	}
	if strings.Contains(response.Body.String(), "providerId") {
		t.Fatalf("provider identity leaked into response: %s", response.Body.String())
	}
	if !strings.Contains(response.Body.String(), `"idempotentReplay":false`) {
		t.Fatalf("response = %s", response.Body.String())
	}

	t.Run("providerId body field must match authenticated identity", func(t *testing.T) {
		before := captured
		request := httptest.NewRequest(http.MethodPost, "/wagering/transactions", strings.NewReader(`{"providerId":"provider-beta","externalTransactionId":"external-2","walletId":"00000000-0000-0000-0000-000000000001","playerId":"player-1","gameId":"game-1","roundId":"round-1","kind":"BET","money":{"amount":"25.00","currency":"BRL"}}`))
		request.Header.Set("Authorization", "Bearer provider-token")
		request.Header.Set("Idempotency-Key", "idem-2")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusForbidden || captured.ID != before.ID {
			t.Fatalf("response = %d %s, captured changed = %v", response.Code, response.Body.String(), captured.ID != before.ID)
		}
	})

	t.Run("opening is not an external operation", func(t *testing.T) {
		before := captured
		openingBody := `{"providerId":"provider-alpha","externalTransactionId":"external-opening","walletId":"00000000-0000-0000-0000-000000000001","playerId":"player-1","gameId":"game-1","roundId":"round-1","kind":"OPENING","money":{"amount":"25.00","currency":"BRL"}}`
		request := httptest.NewRequest(http.MethodPost, "/wagering/transactions", strings.NewReader(openingBody))
		request.Header.Set("Authorization", "Bearer provider-token")
		request.Header.Set("Idempotency-Key", "idem-opening")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusBadRequest || captured.ID != before.ID {
			t.Fatalf("response = %d %s, captured changed = %v", response.Code, response.Body.String(), captured.ID != before.ID)
		}
	})

	t.Run("legacy aliases are not accepted", func(t *testing.T) {
		request := httptest.NewRequest(http.MethodPost, "/wagering/transactions", strings.NewReader(`{"externalId":"external-legacy","walletId":"00000000-0000-0000-0000-000000000001","playerId":"player-1","gameId":"game-1","roundId":"round-1","type":"BET","amount":{"amount":"25.00","currency":"BRL"}}`))
		request.Header.Set("Authorization", "Bearer provider-token")
		request.Header.Set("Idempotency-Key", "idem-legacy")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
		}
	})
}

func TestReconciliationResponseUsesNormativeFields(t *testing.T) {
	walletID := uuid.New()
	financialService := &fakeFinancial{
		recon: func(_ context.Context, id uuid.UUID) (financial.Reconciliation, error) {
			if id != walletID {
				return financial.Reconciliation{}, errors.New("unexpected wallet")
			}
			return financial.Reconciliation{WalletBalance: 10000, LedgerBalance: 7500, Consistent: false}, nil
		},
	}
	queries := &fakeQueries{
		wallet: func(_ context.Context, id uuid.UUID) (query.WalletView, error) {
			return query.WalletView{ID: id, Currency: "BRL", BalanceMinor: 10000}, nil
		},
		ledgerEntryCount: func(_ context.Context, id uuid.UUID) (int64, error) {
			if id != walletID {
				return 0, errors.New("unexpected wallet")
			}
			return 3, nil
		},
	}
	auth := &fakeAuthenticator{identities: map[string]keycloak.Identity{
		"internal-token": testIdentity("wallet-admin", keycloak.RoleInternal),
	}}
	handler := testRouter(auth, financialService, queries)
	request := httptest.NewRequest(http.MethodPost, "/wallets/"+walletID.String()+"/reconciliation", nil)
	request.Header.Set("Authorization", "Bearer internal-token")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"walletId", "storedBalance", "calculatedBalance", "difference", "consistent", "checkedEntries"} {
		if !strings.Contains(string(body), `"`+field+`"`) {
			t.Fatalf("response missing %s: %s", field, body)
		}
	}
	if strings.Contains(string(body), "walletBalance") || strings.Contains(string(body), "ledgerBalance") {
		t.Fatalf("response contains legacy reconciliation fields: %s", body)
	}
}

func TestProcessTransactionRejectsInvalidTransportInputBeforeUseCase(t *testing.T) {
	processCalls := 0
	financialService := &fakeFinancial{
		process: func(_ context.Context, _ financial.Command, _ time.Time) (financial.Result, error) {
			processCalls++
			return financial.Result{}, errors.New("financial use case must not be called")
		},
	}
	auth := &fakeAuthenticator{identities: map[string]keycloak.Identity{
		"provider-token": testIdentity("provider-alpha", keycloak.RoleProvider),
	}}
	handler := testRouter(auth, financialService, &fakeQueries{})
	validBody := `{"providerId":"provider-alpha","externalTransactionId":"external-1","walletId":"00000000-0000-0000-0000-000000000001","playerId":"player-1","gameId":"game-1","roundId":"round-1","kind":"BET","money":{"amount":"25.00","currency":"BRL"}}`
	tests := []struct {
		name  string
		body  string
		setup func(*http.Request)
	}{
		{name: "missing idempotency key", body: validBody, setup: func(*http.Request) {}},
		{name: "malformed JSON", body: "{", setup: func(request *http.Request) {
			request.Header.Set("Idempotency-Key", "idem-malformed")
		}},
		{name: "money without two decimals", body: strings.Replace(validBody, "25.00", "25", 1), setup: func(request *http.Request) {
			request.Header.Set("Idempotency-Key", "idem-invalid-money")
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "/wagering/transactions", strings.NewReader(test.body))
			request.Header.Set("Authorization", "Bearer provider-token")
			test.setup(request)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
			}
		})
	}
	if processCalls != 0 {
		t.Fatalf("financial Process calls = %d, want 0", processCalls)
	}
}

func TestMapErrorClassifiesOIDCOutageAndFinancialInput(t *testing.T) {
	dependency := mapError(keycloak.ErrVerifierUnavailable)
	if dependency.status != http.StatusServiceUnavailable || dependency.code != codeDependencyUnavailable {
		t.Fatalf("OIDC dependency mapping = %+v", dependency)
	}
	invalid := mapError(wager.ErrInvalidTransaction)
	if invalid.status != http.StatusBadRequest || invalid.code != codeInvalidRequest {
		t.Fatalf("financial validation mapping = %+v", invalid)
	}
}

func TestReadinessAndMethodErrors(t *testing.T) {
	auth := &fakeAuthenticator{identities: map[string]keycloak.Identity{}}
	handler := testRouter(auth, &fakeFinancial{}, &fakeQueries{}, fakeChecker{name: "postgres", err: errors.New("down")})

	request := httptest.NewRequest(http.MethodGet, "/health/ready", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable || !strings.Contains(response.Body.String(), `"postgres":"DOWN"`) {
		t.Fatalf("readiness = %d %s", response.Code, response.Body.String())
	}

	request = httptest.NewRequest(http.MethodPost, "/health/live", nil)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusMethodNotAllowed || response.Header().Get("Allow") != http.MethodGet {
		t.Fatalf("method response = %d allow=%q body=%s", response.Code, response.Header().Get("Allow"), response.Body.String())
	}
}

func TestServerDrainsInFlightRequestBeforeShutdown(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	server := NewServer("127.0.0.1:0", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		close(started)
		<-release
		w.WriteHeader(http.StatusNoContent)
	}), testLogger())
	if err := server.Listen(); err != nil {
		t.Fatal(err)
	}
	server.Serve()

	responseCh := make(chan error, 1)
	go func() {
		response, err := http.Get("http://" + server.Addr())
		if err == nil {
			response.Body.Close()
		}
		responseCh <- err
	}()
	<-started

	shutdownCh := make(chan error, 1)
	go func() {
		shutdownCh <- server.Shutdown(context.Background())
	}()
	select {
	case err := <-shutdownCh:
		t.Fatalf("shutdown completed before in-flight request released: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	if err := <-responseCh; err != nil {
		t.Fatal(err)
	}
	if err := <-shutdownCh; err != nil {
		t.Fatal(err)
	}
	if err := server.Close(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		t.Fatal(err)
	}
}

func TestServerForcesCloseWhenDrainTimeoutExpires(t *testing.T) {
	started := make(chan struct{})
	canceled := make(chan struct{})
	server := NewServer("127.0.0.1:0", http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		close(started)
		<-request.Context().Done()
		close(canceled)
	}), testLogger())
	if err := server.Listen(); err != nil {
		t.Fatal(err)
	}
	server.Serve()

	responseCh := make(chan error, 1)
	go func() {
		response, err := http.Get("http://" + server.Addr())
		if response != nil {
			response.Body.Close()
		}
		responseCh <- err
	}()
	<-started

	drainCtx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := server.Shutdown(drainCtx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("shutdown error = %v, want context deadline exceeded", err)
	}
	if err := server.Close(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		t.Fatal(err)
	}
	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("in-flight request was not canceled after forced close")
	}
	select {
	case <-responseCh:
	case <-time.After(time.Second):
		t.Fatal("client did not observe forced close")
	}
}
