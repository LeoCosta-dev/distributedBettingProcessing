package financial

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	"github.com/google/uuid"
	"github.com/leonardodacosta/distributedBettingProcessing/internal/domain/money"
	"github.com/leonardodacosta/distributedBettingProcessing/internal/domain/wager"
)

// canonicalCommand contains only business input. Transport metadata, the
// internal command ID, the idempotency key and a caller-provided hash are
// deliberately excluded so equivalent requests produce the same digest.
type canonicalCommand struct {
	ExternalID          string      `json:"externalId"`
	ProviderID          string      `json:"providerId"`
	WalletID            uuid.UUID   `json:"walletId"`
	PlayerID            string      `json:"playerId"`
	GameID              string      `json:"gameId"`
	RoundID             string      `json:"roundId"`
	ReferenceExternalID string      `json:"referenceExternalId,omitempty"`
	Type                wager.Type  `json:"type"`
	Amount              money.Money `json:"amount"`
}

func canonicalPayload(cmd Command) ([]byte, error) {
	return json.Marshal(canonicalCommand{
		ExternalID:          cmd.ExternalID,
		ProviderID:          cmd.ProviderID,
		WalletID:            cmd.WalletID,
		PlayerID:            cmd.PlayerID,
		GameID:              cmd.GameID,
		RoundID:             cmd.RoundID,
		ReferenceExternalID: cmd.ReferenceExternalID,
		Type:                cmd.Type,
		Amount:              cmd.Amount,
	})
}

func canonicalPayloadHash(cmd Command) (string, error) {
	payload, err := canonicalPayload(cmd)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:]), nil
}

type persistedResult struct {
	TransactionID uuid.UUID   `json:"transactionId"`
	State         wager.State `json:"state"`
	Balance       int64       `json:"balance"`
	Amount        money.Money `json:"amount"`
}

func marshalResult(result Result) ([]byte, error) {
	return json.Marshal(persistedResult{
		TransactionID: result.TransactionID,
		State:         result.State,
		Balance:       result.Balance,
		Amount:        result.Amount,
	})
}

func unmarshalResult(data []byte) (Result, error) {
	var persisted persistedResult
	if err := json.Unmarshal(data, &persisted); err != nil {
		return Result{}, err
	}
	return Result{
		TransactionID: persisted.TransactionID,
		State:         persisted.State,
		Balance:       persisted.Balance,
		Amount:        persisted.Amount,
	}, nil
}
