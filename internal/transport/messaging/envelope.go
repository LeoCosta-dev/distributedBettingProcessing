package messaging

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/leonardodacosta/distributedBettingProcessing/internal/application/financial"
	"github.com/leonardodacosta/distributedBettingProcessing/internal/domain/money"
	"github.com/leonardodacosta/distributedBettingProcessing/internal/domain/wager"
)

var (
	ErrMalformedMessage       = errors.New("malformed SQS message")
	ErrUnsupportedMessageType = errors.New("unsupported SQS message type")
)

const wagerTransactionRequested = "WagerTransactionRequested"

type messageEnvelope struct {
	MessageID  string             `json:"messageId"`
	Type       string             `json:"type"`
	OccurredAt string             `json:"occurredAt"`
	Data       messageTransaction `json:"data"`
}

type messageTransaction struct {
	ProviderID          string    `json:"providerId"`
	ExternalID          string    `json:"externalTransactionId"`
	IdempotencyKey      string    `json:"idempotencyKey"`
	PlayerID            string    `json:"playerId"`
	WalletID            string    `json:"walletId"`
	RoundID             string    `json:"roundId"`
	GameID              string    `json:"gameId"`
	Kind                string    `json:"kind"`
	ReferenceExternalID string    `json:"referenceExternalTransactionId,omitempty"`
	Money               moneyWire `json:"money"`
}

type moneyWire struct {
	Amount   string `json:"amount"`
	Currency string `json:"currency"`
}

type DecodedMessage struct {
	MessageID   string
	OccurredAt  time.Time
	Command     financial.Command
	PayloadHash string
}

func DecodeMessage(body string) (DecodedMessage, error) {
	var envelope messageEnvelope
	decoder := json.NewDecoder(strings.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&envelope); err != nil {
		return DecodedMessage{}, fmt.Errorf("%w: %v", ErrMalformedMessage, err)
	}
	var extra json.RawMessage
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return DecodedMessage{}, fmt.Errorf("%w: message must contain one JSON document", ErrMalformedMessage)
	}

	messageID := strings.TrimSpace(envelope.MessageID)
	if messageID == "" {
		return DecodedMessage{}, fmt.Errorf("%w: messageId is required", ErrMalformedMessage)
	}
	if strings.TrimSpace(envelope.Type) != wagerTransactionRequested {
		return DecodedMessage{}, fmt.Errorf("%w: %q", ErrUnsupportedMessageType, envelope.Type)
	}
	occurredAt, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(envelope.OccurredAt))
	if err != nil {
		return DecodedMessage{}, fmt.Errorf("%w: occurredAt must be RFC3339: %v", ErrMalformedMessage, err)
	}
	providerID := strings.TrimSpace(envelope.Data.ProviderID)
	externalID := strings.TrimSpace(envelope.Data.ExternalID)
	idempotencyKey := strings.TrimSpace(envelope.Data.IdempotencyKey)
	playerID := strings.TrimSpace(envelope.Data.PlayerID)
	roundID := strings.TrimSpace(envelope.Data.RoundID)
	gameID := strings.TrimSpace(envelope.Data.GameID)
	for field, value := range map[string]string{
		"data.providerId":            providerID,
		"data.externalTransactionId": externalID,
		"data.idempotencyKey":        idempotencyKey,
		"data.playerId":              playerID,
		"data.roundId":               roundID,
		"data.gameId":                gameID,
	} {
		if value == "" {
			return DecodedMessage{}, fmt.Errorf("%w: %s is required", ErrMalformedMessage, field)
		}
	}
	walletID, err := uuid.Parse(strings.TrimSpace(envelope.Data.WalletID))
	if err != nil {
		return DecodedMessage{}, fmt.Errorf("%w: data.walletId must be a UUID", ErrMalformedMessage)
	}
	amount, err := money.New(envelope.Data.Money.Amount, envelope.Data.Money.Currency)
	if err != nil {
		return DecodedMessage{}, fmt.Errorf("%w: data.money: %v", ErrMalformedMessage, err)
	}
	typ, err := messageType(envelope.Data.Kind)
	if err != nil {
		return DecodedMessage{}, err
	}
	command := financial.Command{
		ID:                  uuid.New(),
		WalletID:            walletID,
		ExternalID:          externalID,
		ProviderID:          providerID,
		PlayerID:            playerID,
		GameID:              gameID,
		RoundID:             roundID,
		IdempotencyKey:      idempotencyKey,
		ReferenceExternalID: strings.TrimSpace(envelope.Data.ReferenceExternalID),
		Type:                typ,
		Amount:              amount,
	}
	payloadHash, err := financial.CanonicalPayloadHash(command)
	if err != nil {
		return DecodedMessage{}, fmt.Errorf("%w: canonical payload: %v", ErrMalformedMessage, err)
	}
	return DecodedMessage{MessageID: messageID, OccurredAt: occurredAt, Command: command, PayloadHash: payloadHash}, nil
}

func messageType(raw string) (wager.Type, error) {
	switch typ := wager.Type(strings.TrimSpace(raw)); typ {
	case wager.Bet, wager.Win, wager.Loss, wager.Refund, wager.Rollback:
		return typ, nil
	case wager.Opening:
		return "", fmt.Errorf("%w: OPENING is internal only", ErrMalformedMessage)
	default:
		return "", fmt.Errorf("%w: %q", ErrUnsupportedMessageType, raw)
	}
}
