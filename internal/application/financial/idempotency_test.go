package financial

import (
	"bytes"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/leonardodacosta/distributedBettingProcessing/internal/domain/wager"
	"github.com/leonardodacosta/distributedBettingProcessing/internal/infrastructure/postgres"
)

func TestCanonicalPayloadExcludesRequestMetadata(t *testing.T) {
	amount := moneyMust("12.34", "BRL")
	base := Command{
		ID: uuid.New(), WalletID: uuid.New(), ExternalID: "external-1", ProviderID: "provider-1",
		PlayerID: "player-1", GameID: "game-1", RoundID: "round-1", IdempotencyKey: "key-1",
		PayloadHash: "caller-hash-1", Type: wager.Bet, Amount: amount,
	}
	other := base
	other.ID = uuid.New()
	other.IdempotencyKey = "key-2"
	other.PayloadHash = "caller-hash-2"

	first, err := canonicalPayload(base)
	if err != nil {
		t.Fatal(err)
	}
	second, err := canonicalPayload(other)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) {
		t.Fatalf("canonical payload changed with request metadata: %s != %s", first, second)
	}

	firstHash, err := canonicalPayloadHash(base)
	if err != nil {
		t.Fatal(err)
	}
	secondHash, err := canonicalPayloadHash(other)
	if err != nil {
		t.Fatal(err)
	}
	if firstHash != secondHash {
		t.Fatalf("canonical hash changed with request metadata: %s != %s", firstHash, secondHash)
	}
}

func TestCanonicalPayloadChangesForBusinessFields(t *testing.T) {
	base := Command{
		ID: uuid.New(), WalletID: uuid.New(), ExternalID: "external-1", ProviderID: "provider-1",
		PlayerID: "player-1", GameID: "game-1", RoundID: "round-1", IdempotencyKey: "key-1",
		Type: wager.Bet, Amount: moneyMust("12.34", "BRL"),
	}
	for name, mutate := range map[string]func(*Command){
		"external": func(cmd *Command) { cmd.ExternalID = "external-2" },
		"wallet":   func(cmd *Command) { cmd.WalletID = uuid.New() },
		"player":   func(cmd *Command) { cmd.PlayerID = "player-2" },
		"game":     func(cmd *Command) { cmd.GameID = "game-2" },
		"round":    func(cmd *Command) { cmd.RoundID = "round-2" },
		"reference": func(cmd *Command) {
			cmd.ReferenceExternalID = "reference-1"
		},
		"type":     func(cmd *Command) { cmd.Type = wager.Win },
		"amount":   func(cmd *Command) { cmd.Amount = moneyMust("12.35", "BRL") },
		"currency": func(cmd *Command) { cmd.Amount = moneyMust("12.34", "USD") },
	} {
		t.Run(name, func(t *testing.T) {
			changed := base
			mutate(&changed)
			first, err := canonicalPayloadHash(base)
			if err != nil {
				t.Fatal(err)
			}
			second, err := canonicalPayloadHash(changed)
			if err != nil {
				t.Fatal(err)
			}
			if first == second {
				t.Fatalf("business field %s did not change payload hash", name)
			}
		})
	}
}

func TestReplayResultRejectsMissingOrInconsistentSnapshots(t *testing.T) {
	record := postgresRecordForReplay()
	if _, err := replayResult(record); !errors.Is(err, ErrReplayUnavailable) {
		t.Fatalf("missing result error = %v", err)
	}

	record.Result, _ = marshalResult(Result{TransactionID: uuid.New(), State: wager.Processed, Balance: 1, Amount: moneyMust("0.01", "BRL")})
	if _, err := replayResult(record); !errors.Is(err, ErrReplayUnavailable) {
		t.Fatalf("inconsistent result error = %v", err)
	}
}

func TestValidateCommandRequiresPersistentIdempotencyKey(t *testing.T) {
	cmd := Command{
		ID: uuid.New(), WalletID: uuid.New(), ExternalID: "external", ProviderID: "provider",
		PlayerID: "player", GameID: "game", RoundID: "round", Type: wager.Bet,
		Amount: moneyMust("1.00", "BRL"),
	}
	if !errors.Is(validateCommand(cmd), ErrInvalidCommand) {
		t.Fatal("missing Idempotency-Key was accepted")
	}
}

func TestKnownIdentityViolationOnlyClassifiesExpectedPostgresConstraints(t *testing.T) {
	known := &pgconn.PgError{Code: "23505", ConstraintName: "wager_transactions_provider_id_idempotency_key_key"}
	if !isKnownIdentityViolation(known) {
		t.Fatal("known idempotency constraint was not classified")
	}
	unknownConstraint := &pgconn.PgError{Code: "23505", ConstraintName: "wallets_player_id_currency_key"}
	if isKnownIdentityViolation(unknownConstraint) {
		t.Fatal("unrelated unique constraint was classified as idempotency")
	}
	nonUnique := &pgconn.PgError{Code: "23503", ConstraintName: "wager_transactions_wallet_id_fkey"}
	if isKnownIdentityViolation(nonUnique) {
		t.Fatal("non-unique PostgreSQL error was classified as idempotency")
	}
}

func postgresRecordForReplay() postgres.WagerTransactionRecord {
	return postgres.WagerTransactionRecord{ID: uuid.New(), State: string(wager.Processed)}
}
