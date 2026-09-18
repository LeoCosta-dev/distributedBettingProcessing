package messaging

import (
	"errors"
	"testing"

	"github.com/leonardodacosta/distributedBettingProcessing/internal/application/financial"
	"github.com/leonardodacosta/distributedBettingProcessing/internal/domain/wager"
)

const validMessage = `{
  "messageId": "msg-123",
  "type": "WagerTransactionRequested",
  "occurredAt": "2026-09-08T12:00:00.000Z",
  "data": {
    "providerId": "provider-a",
    "externalTransactionId": "transaction-123",
    "idempotencyKey": "provider-a:transaction-123",
    "playerId": "player-1",
    "walletId": "0192f291-27dd-7d3f-8071-5f8685deef37",
    "roundId": "round-987",
    "gameId": "fortune-chimp",
    "kind": "BET",
    "money": { "amount": "25.00", "currency": "BRL" }
  }
}`

func TestDecodeMessageBuildsTheSharedFinancialCommand(t *testing.T) {
	decoded, err := DecodeMessage(validMessage)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.MessageID != "msg-123" || decoded.Command.ProviderID != "provider-a" || decoded.Command.Type != wager.Bet {
		t.Fatalf("decoded message = %+v", decoded)
	}
	if decoded.Command.Amount.Minor() != 2500 || decoded.Command.Amount.Currency() != "BRL" {
		t.Fatalf("decoded amount = %s", decoded.Command.Amount)
	}
	hash, err := financial.CanonicalPayloadHash(decoded.Command)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.PayloadHash != hash {
		t.Fatalf("payload hash = %q, want %q", decoded.PayloadHash, hash)
	}
}

func TestDecodeMessageRejectsMalformedAndInternalMessages(t *testing.T) {
	tests := []struct {
		name string
		body string
		err  error
	}{
		{name: "unknown field", body: replaceMessageValue(validMessage, `"data": {`, `"data": {"unexpected":true,`), err: ErrMalformedMessage},
		{name: "opening", body: replaceMessageValue(validMessage, `"kind": "BET"`, `"kind": "OPENING"`), err: ErrMalformedMessage},
		{name: "scientific amount", body: replaceMessageValue(validMessage, `"amount": "25.00"`, `"amount": "2.5e1"`), err: ErrMalformedMessage},
		{name: "wrong type", body: replaceMessageValue(validMessage, `"type": "WagerTransactionRequested"`, `"type": "Other"`), err: ErrUnsupportedMessageType},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := DecodeMessage(test.body)
			if !errors.Is(err, test.err) {
				t.Fatalf("error = %v, want %v", err, test.err)
			}
		})
	}
}

func replaceMessageValue(body, old, replacement string) string {
	for index := 0; index+len(old) <= len(body); index++ {
		if body[index:index+len(old)] == old {
			return body[:index] + replacement + body[index+len(old):]
		}
	}
	return body
}
