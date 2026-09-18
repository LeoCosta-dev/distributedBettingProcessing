package http

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/leonardodacosta/distributedBettingProcessing/internal/application/financial"
	"github.com/leonardodacosta/distributedBettingProcessing/internal/application/query"
	"github.com/leonardodacosta/distributedBettingProcessing/internal/domain/money"
	"github.com/leonardodacosta/distributedBettingProcessing/internal/domain/wager"
	"github.com/leonardodacosta/distributedBettingProcessing/internal/infrastructure/keycloak"
)

// errorCode is the machine readable error identifier exposed on the wire.
type errorCode string

const (
	codeInvalidRequest             errorCode = "INVALID_REQUEST"
	codeInvalidAmount              errorCode = "INVALID_AMOUNT"
	codeUnsupportedTransactionType errorCode = "UNSUPPORTED_TRANSACTION_TYPE"
	codeOpeningNotAllowed          errorCode = "OPENING_NOT_ALLOWED"
	codeUnauthenticated            errorCode = "UNAUTHENTICATED"
	codeForbidden                  errorCode = "FORBIDDEN"
	codeNotFound                   errorCode = "NOT_FOUND"
	codeMethodNotAllowed           errorCode = "METHOD_NOT_ALLOWED"
	codeIdempotencyConflict        errorCode = "IDEMPOTENCY_CONFLICT"
	codeReplayUnavailable          errorCode = "REPLAY_UNAVAILABLE"
	codeWalletAlreadyExists        errorCode = "WALLET_ALREADY_EXISTS"
	codeDependencyUnavailable      errorCode = "DEPENDENCY_UNAVAILABLE"
	codeInternalError              errorCode = "INTERNAL_ERROR"
)

// Transport level sentinels. They carry no financial semantics: they only
// describe why a request could not be translated into an application command.
var (
	errInvalidRequest        = errors.New("invalid request")
	errUnsupportedType       = errors.New("unsupported transaction type")
	errOpeningNotAllowed     = errors.New("opening is not an external operation")
	errUnauthenticated       = errors.New("unauthenticated")
	errForbidden             = errors.New("forbidden")
	errProviderIdentity      = errors.New("provider identity is not established")
	errWalletAlreadyExists   = errors.New("wallet already exists")
	errDependencyUnavailable = errors.New("dependency unavailable")
	// errRouteNotFound and errMethodNotAllowed describe routing failures.
	errRouteNotFound    = errors.New("route not found")
	errMethodNotAllowed = errors.New("method not allowed")
	// errInvalidPersistedValue reports a stored value the wire cannot represent.
	// It is an internal failure, never a client error.
	errInvalidPersistedValue = errors.New("persisted value cannot be represented")
)

type errorEnvelope struct {
	Error errorBody `json:"error"`
}

type errorBody struct {
	Code    errorCode `json:"code"`
	Message string    `json:"message"`
}

type mappedError struct {
	status  int
	code    errorCode
	message string
}

func badRequest(message string) error {
	return fmt.Errorf("%w: %s", errInvalidRequest, message)
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if payload == nil {
		return
	}
	encoder := json.NewEncoder(w)
	encoder.SetEscapeHTML(false)
	_ = encoder.Encode(payload)
}

// writeError maps a Go error to the uniform error envelope.
//
// Persisted business states (REJECTED, PENDING_REFERENCE) never reach this
// function: they are successful, durably recorded financial outcomes and are
// returned as a normal result payload.
func writeError(w http.ResponseWriter, r *http.Request, logger *slog.Logger, err error) {
	mapped := mapError(err)
	if mapped.status >= http.StatusInternalServerError && logger != nil {
		logger.Error("request failed",
			slog.String("method", r.Method),
			slog.String("path", r.URL.Path),
			slog.Int("status", mapped.status),
			slog.String("code", string(mapped.code)),
			slog.String("error", err.Error()),
		)
	}
	writeJSON(w, mapped.status, errorEnvelope{Error: errorBody{Code: mapped.code, Message: mapped.message}})
}

func mapError(err error) mappedError {
	switch {
	case err == nil:
		return mappedError{status: http.StatusInternalServerError, code: codeInternalError, message: "unexpected error"}
	case errors.Is(err, keycloak.ErrVerifierUnavailable):
		return mappedError{status: http.StatusServiceUnavailable, code: codeDependencyUnavailable, message: "authentication dependency unavailable"}
	case errors.Is(err, keycloak.ErrInvalidToken):
		return mappedError{status: http.StatusUnauthorized, code: codeUnauthenticated, message: "invalid or missing bearer token"}
	case errors.Is(err, errUnauthenticated):
		return mappedError{status: http.StatusUnauthorized, code: codeUnauthenticated, message: "authentication required"}
	case errors.Is(err, errProviderIdentity):
		return mappedError{status: http.StatusUnauthorized, code: codeUnauthenticated, message: "token does not establish a provider identity"}
	case errors.Is(err, errForbidden):
		return mappedError{status: http.StatusForbidden, code: codeForbidden, message: "forbidden"}
	case errors.Is(err, query.ErrWalletNotFound), errors.Is(err, query.ErrTransactionNotFound), errors.Is(err, pgx.ErrNoRows):
		return mappedError{status: http.StatusNotFound, code: codeNotFound, message: "resource not found"}
	case errors.Is(err, query.ErrInvalidCursor), errors.Is(err, query.ErrInvalidLimit):
		return mappedError{status: http.StatusBadRequest, code: codeInvalidRequest, message: err.Error()}
	case errors.Is(err, financial.ErrInvalidCommand), errors.Is(err, financial.ErrReferenceInvalid), errors.Is(err, wager.ErrInvalidTransaction):
		return mappedError{status: http.StatusBadRequest, code: codeInvalidRequest, message: err.Error()}
	case errors.Is(err, financial.ErrIdempotencyConflict):
		return mappedError{status: http.StatusConflict, code: codeIdempotencyConflict, message: "idempotency identity conflict"}
	case errors.Is(err, financial.ErrReplayUnavailable):
		return mappedError{status: http.StatusConflict, code: codeReplayUnavailable, message: "persisted result unavailable for replay"}
	case errors.Is(err, errWalletAlreadyExists):
		return mappedError{status: http.StatusConflict, code: codeWalletAlreadyExists, message: "wallet already exists for player and currency"}
	case errors.Is(err, errRouteNotFound):
		return mappedError{status: http.StatusNotFound, code: codeNotFound, message: "route not found"}
	case errors.Is(err, errMethodNotAllowed):
		return mappedError{status: http.StatusMethodNotAllowed, code: codeMethodNotAllowed, message: "method not allowed"}
	case errors.Is(err, money.ErrInvalidAmount), errors.Is(err, money.ErrInvalidCurrency), errors.Is(err, money.ErrOverflow):
		return mappedError{status: http.StatusBadRequest, code: codeInvalidAmount, message: err.Error()}
	case errors.Is(err, errUnsupportedType):
		return mappedError{status: http.StatusBadRequest, code: codeUnsupportedTransactionType, message: err.Error()}
	case errors.Is(err, errOpeningNotAllowed):
		return mappedError{status: http.StatusBadRequest, code: codeOpeningNotAllowed, message: err.Error()}
	case errors.Is(err, errInvalidRequest):
		return mappedError{status: http.StatusBadRequest, code: codeInvalidRequest, message: err.Error()}
	case errors.Is(err, errInvalidPersistedValue):
		return mappedError{status: http.StatusInternalServerError, code: codeInternalError, message: "persisted value cannot be represented"}
	case errors.Is(err, errDependencyUnavailable), errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		return mappedError{status: http.StatusServiceUnavailable, code: codeDependencyUnavailable, message: "dependency unavailable"}
	case isConnectionFailure(err):
		return mappedError{status: http.StatusServiceUnavailable, code: codeDependencyUnavailable, message: "dependency unavailable"}
	case isWalletUniqueViolation(err):
		return mappedError{status: http.StatusConflict, code: codeWalletAlreadyExists, message: "wallet already exists for player and currency"}
	default:
		return mappedError{status: http.StatusInternalServerError, code: codeInternalError, message: "internal error"}
	}
}

// isWalletUniqueViolation recognizes the only PostgreSQL unique violation the
// transport classifies: the (player_id, currency) wallet identity. It is not a
// consistency mechanism; the database constraint remains authoritative.
func isWalletUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return false
	}
	return pgErr.Code == "23505" && pgErr.ConstraintName == "wallets_player_id_currency_key"
}

// isConnectionFailure recognizes PostgreSQL connectivity failures and operator
// intervention errors, which are transient from the caller's point of view.
func isConnectionFailure(err error) bool {
	var connectErr *pgconn.ConnectError
	if errors.As(err, &connectErr) {
		return true
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && len(pgErr.Code) == 5 {
		switch pgErr.Code[:2] {
		case "08", "53", "57":
			return true
		}
	}
	return false
}
