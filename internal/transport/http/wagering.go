package http

import (
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/leonardodacosta/distributedBettingProcessing/internal/application/financial"
	"github.com/leonardodacosta/distributedBettingProcessing/internal/application/query"
)

// idempotencyKeyHeader carries the caller supplied idempotency key for external
// financial processing. The key is transport metadata: it belongs to the
// request, not to the financial payload.
const idempotencyKeyHeader = "Idempotency-Key"

// processTransaction handles POST /wagering/transactions.
//
// It only translates transport data into a financial command: the command
// identity is generated here and is never accepted from the client, the
// provider identity comes exclusively from the authenticated token, and the
// payload hash is computed by the application, not sent by the caller.
func (r *Router) processTransaction(w http.ResponseWriter, request *http.Request) {
	providerID, err := providerIdentity(request)
	if err != nil {
		r.writeError(w, request, err)
		return
	}
	idempotencyKey := strings.TrimSpace(request.Header.Get(idempotencyKeyHeader))
	if idempotencyKey == "" {
		r.writeError(w, request, badRequest(idempotencyKeyHeader+" header is required"))
		return
	}
	var body wageringTransactionRequest
	if err := decodeJSON(w, request, &body); err != nil {
		r.writeError(w, request, err)
		return
	}
	walletID, err := parseUUID("walletId", body.WalletID)
	if err != nil {
		r.writeError(w, request, err)
		return
	}
	claimedProviderID, err := required("providerId", body.ProviderID)
	if err != nil {
		r.writeError(w, request, err)
		return
	}
	if claimedProviderID != providerID {
		r.writeError(w, request, errForbidden)
		return
	}
	externalID, err := required("externalTransactionId", body.ExternalID)
	if err != nil {
		r.writeError(w, request, err)
		return
	}
	playerID, err := required("playerId", body.PlayerID)
	if err != nil {
		r.writeError(w, request, err)
		return
	}
	gameID, err := required("gameId", body.GameID)
	if err != nil {
		r.writeError(w, request, err)
		return
	}
	roundID, err := required("roundId", body.RoundID)
	if err != nil {
		r.writeError(w, request, err)
		return
	}
	operationType, err := externalType(body.Type)
	if err != nil {
		r.writeError(w, request, err)
		return
	}
	amount, err := body.Amount.toMoney("money")
	if err != nil {
		r.writeError(w, request, err)
		return
	}
	command := financial.Command{
		// One identity per HTTP attempt. Replays are resolved by the
		// application through the provider scoped idempotency key, never by a
		// client supplied transaction identity.
		ID:                  uuid.New(),
		WalletID:            walletID,
		ExternalID:          externalID,
		ProviderID:          providerID,
		PlayerID:            playerID,
		GameID:              gameID,
		RoundID:             roundID,
		IdempotencyKey:      idempotencyKey,
		ReferenceExternalID: strings.TrimSpace(body.ReferenceExternalID),
		Type:                operationType,
		Amount:              amount,
	}
	result, err := r.financial.Process(request.Context(), command, time.Now().UTC())
	if err != nil {
		r.writeError(w, request, err)
		return
	}
	replay := isIdempotentReplay(result, command.ID)
	response, err := resultResponse(result, &replay)
	if err != nil {
		r.writeError(w, request, err)
		return
	}
	writeJSON(w, http.StatusOK, response)
}

// isIdempotentReplay reports whether the application returned the persisted
// outcome of an earlier attempt instead of a new one.
//
// This inference relies on the current semantics of financial.Service: a new
// attempt always commits the command identity it received, while a replay
// returns the identity of the record that was already persisted for the same
// provider scoped idempotency key. The adapter is the only component that
// generates the command identity, so the comparison is unambiguous here. It
// must be revisited if that application guarantee ever changes.
func isIdempotentReplay(result financial.Result, commandID uuid.UUID) bool {
	return result.TransactionID != commandID
}

// transactionByID handles GET /wagering/transactions/{transactionId}. The read
// is provider filtered by PostgreSQL, so another provider's transaction is
// indistinguishable from a missing one.
func (r *Router) transactionByID(w http.ResponseWriter, request *http.Request) {
	providerID, err := providerIdentity(request)
	if err != nil {
		r.writeError(w, request, err)
		return
	}
	transactionID, err := parseUUID("transactionId", request.PathValue("transactionId"))
	if err != nil {
		r.writeError(w, request, err)
		return
	}
	view, err := r.queries.TransactionForProvider(request.Context(), providerID, transactionID)
	if err != nil {
		r.writeError(w, request, err)
		return
	}
	response, err := transactionViewResponse(view)
	if err != nil {
		r.writeError(w, request, err)
		return
	}
	writeJSON(w, http.StatusOK, response)
}

// transactionByExternalID handles
// GET /providers/{providerId}/wagering/transactions/{externalTransactionId}.
//
// The path provider is confronted with the authenticated provider before any
// data is read. A mismatch is forbidden; an external identity that belongs to
// another provider is simply not found.
func (r *Router) transactionByExternalID(w http.ResponseWriter, request *http.Request) {
	providerID, err := providerIdentity(request)
	if err != nil {
		r.writeError(w, request, err)
		return
	}
	if pathProviderID := strings.TrimSpace(request.PathValue("providerId")); pathProviderID != providerID {
		r.writeError(w, request, errForbidden)
		return
	}
	externalID, err := required("externalTransactionId", request.PathValue("externalTransactionId"))
	if err != nil {
		r.writeError(w, request, err)
		return
	}
	view, err := r.queries.TransactionByExternalForProvider(request.Context(), providerID, externalID)
	if err != nil {
		r.writeError(w, request, err)
		return
	}
	response, err := transactionViewResponse(view)
	if err != nil {
		r.writeError(w, request, err)
		return
	}
	writeJSON(w, http.StatusOK, response)
}

// resultResponse renders a freshly processed result. idempotentReplay is set
// only by POST /wagering/transactions, the single endpoint able to observe an
// idempotent replay.
func resultResponse(result financial.Result, replay *bool) (wageringResultResponse, error) {
	amount, err := wireMoney(result.Amount.Minor(), result.Amount.Currency())
	if err != nil {
		return wageringResultResponse{}, err
	}
	balance, err := wireMoney(result.Balance, result.Amount.Currency())
	if err != nil {
		return wageringResultResponse{}, err
	}
	return wageringResultResponse{
		TransactionID:    result.TransactionID.String(),
		State:            string(result.State),
		Amount:           amount,
		Balance:          &balance,
		IdempotentReplay: replay,
	}, nil
}

// transactionViewResponse renders a persisted transaction. The balance is the
// snapshot committed with the transaction and is omitted when the record has no
// compatible snapshot; it is never reconstructed from the wallet's current
// balance. The replay flag does not exist on read endpoints.
func transactionViewResponse(view query.TransactionView) (wageringResultResponse, error) {
	amount, err := wireMoney(view.AmountMinor, view.Currency)
	if err != nil {
		return wageringResultResponse{}, err
	}
	response := wageringResultResponse{
		TransactionID: view.ID.String(),
		State:         string(view.State),
		Amount:        amount,
	}
	if view.BalanceMinor != nil {
		balance, err := wireMoney(*view.BalanceMinor, view.Currency)
		if err != nil {
			return wageringResultResponse{}, err
		}
		response.Balance = &balance
	}
	return response, nil
}
