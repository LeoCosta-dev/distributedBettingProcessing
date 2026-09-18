package http

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/leonardodacosta/distributedBettingProcessing/internal/domain/money"
	"github.com/leonardodacosta/distributedBettingProcessing/internal/domain/wager"
)

// maxRequestBodyBytes bounds transport input. Financial sizing, not payload
// sizing, is what the domain protects; this only prevents unbounded reads.
const maxRequestBodyBytes = 64 << 10

// moneyDTO is the external money representation of every request schema.
type moneyDTO struct {
	Amount   string `json:"amount"`
	Currency string `json:"currency"`
}

// invalidAmountError keeps the domain classification (errors.Is works through
// Unwrap) while naming the offending field.
type invalidAmountError struct {
	field string
	err   error
}

func (e invalidAmountError) Error() string { return fmt.Sprintf("%s: %v", e.field, e.err) }
func (e invalidAmountError) Unwrap() error { return e.err }

func (m moneyDTO) toMoney(field string) (money.Money, error) {
	if m.Amount == "" || m.Currency == "" {
		return money.Money{}, badRequest(field + " requires both amount and currency")
	}
	value, err := money.New(m.Amount, m.Currency)
	if err != nil {
		return money.Money{}, invalidAmountError{field: field, err: err}
	}
	return value, nil
}

// wireMoney renders a persisted amount in minor units using the fixed
// two-decimal external representation. The domain constructor validates the
// result, so the transport never formats money by itself.
func wireMoney(minor int64, currency string) (money.Money, error) {
	if minor < 0 {
		return money.Money{}, fmt.Errorf("%w: negative minor value", errInvalidPersistedValue)
	}
	value, err := money.New(fmt.Sprintf("%d.%02d", minor/100, minor%100), currency)
	if err != nil {
		return money.Money{}, fmt.Errorf("%w: %v", errInvalidPersistedValue, err)
	}
	return value, nil
}

func decodeJSON(w http.ResponseWriter, r *http.Request, target any) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBodyBytes)
	decoder := json.NewDecoder(r.Body)
	// Unknown fields are rejected so legacy aliases and typos cannot be silently
	// accepted as part of the public contract.
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		if errors.Is(err, io.EOF) {
			return badRequest("request body is required")
		}
		return badRequest("malformed JSON body: " + err.Error())
	}
	var extra json.RawMessage
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return badRequest("request body must contain a single JSON document")
	}
	return nil
}

type wageringTransactionRequest struct {
	ProviderID          string   `json:"providerId"`
	ExternalID          string   `json:"externalTransactionId"`
	WalletID            string   `json:"walletId"`
	PlayerID            string   `json:"playerId"`
	GameID              string   `json:"gameId"`
	RoundID             string   `json:"roundId"`
	ReferenceExternalID string   `json:"referenceExternalTransactionId"`
	Type                string   `json:"kind"`
	Amount              moneyDTO `json:"money"`
}

type openWalletRequest struct {
	PlayerID       string   `json:"playerId"`
	OpeningBalance moneyDTO `json:"initialBalance"`
}

type wageringResultResponse struct {
	TransactionID string       `json:"transactionId"`
	State         string       `json:"status"`
	Amount        money.Money  `json:"amount"`
	Balance       *money.Money `json:"balance,omitempty"`
	// IdempotentReplay is present only on POST /wagering/transactions, which is
	// the only endpoint that can resolve an existing idempotency record.
	IdempotentReplay *bool `json:"idempotentReplay,omitempty"`
}

type walletResponse struct {
	WalletID  string      `json:"id"`
	PlayerID  string      `json:"playerId"`
	Currency  string      `json:"currency"`
	Balance   money.Money `json:"balance"`
	Version   int64       `json:"version"`
	CreatedAt time.Time   `json:"createdAt"`
	UpdatedAt time.Time   `json:"updatedAt"`
}

type ledgerEntryResponse struct {
	EntryID       string      `json:"entryId"`
	WalletID      string      `json:"walletId"`
	TransactionID string      `json:"transactionId"`
	Direction     string      `json:"direction"`
	Amount        money.Money `json:"amount"`
	BalanceBefore money.Money `json:"balanceBefore"`
	BalanceAfter  money.Money `json:"balanceAfter"`
	Timestamp     time.Time   `json:"timestamp"`
}

type ledgerResponse struct {
	WalletID   string                `json:"walletId"`
	Currency   string                `json:"currency"`
	Entries    []ledgerEntryResponse `json:"entries"`
	NextCursor *string               `json:"nextCursor"`
}

func required(field, value string) (string, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return "", badRequest(field + " is required")
	}
	return trimmed, nil
}

func parseUUID(field, raw string) (uuid.UUID, error) {
	parsed, err := uuid.Parse(strings.TrimSpace(raw))
	if err != nil {
		return uuid.Nil, badRequest(field + " must be a UUID")
	}
	return parsed, nil
}

// externalType maps a transport type value to the external wagering operation.
// OPENING is internal only and must never be reachable through the wire.
func externalType(raw string) (wager.Type, error) {
	switch typ := wager.Type(strings.TrimSpace(raw)); typ {
	case wager.Bet, wager.Win, wager.Loss, wager.Refund, wager.Rollback:
		return typ, nil
	case wager.Opening:
		return "", fmt.Errorf("%w: OPENING is reserved for internal wallet creation", errOpeningNotAllowed)
	case "":
		return "", badRequest("kind is required")
	default:
		return "", fmt.Errorf("%w: %q", errUnsupportedType, raw)
	}
}

type reconciliationResponse struct {
	WalletID          string       `json:"walletId"`
	StoredBalance     money.Money  `json:"storedBalance"`
	CalculatedBalance *money.Money `json:"calculatedBalance"`
	Difference        *money.Money `json:"difference"`
	Consistent        bool         `json:"consistent"`
	CheckedEntries    int64        `json:"checkedEntries"`
}
