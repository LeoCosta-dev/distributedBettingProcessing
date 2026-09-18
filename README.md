# distributedBettingProcessing

Backend challenge implementation for distributed wagering transaction
processing. The system uses Go, Uber Fx, PostgreSQL, SQS through LocalStack,
and Keycloak. Financial correctness is PostgreSQL-backed: exact money,
wallet-scoped locking, durable idempotency, append-only ledger, transactional
inbox and transactional outbox are not delegated to FIFO delivery semantics.

## Status

Loops 7–11 are checkpointed. Loop 12 completed the final validation and is
pending Human Review. The checked-in implementation covers the challenge
scope; the documented limitations below remain deliberate boundaries, not
claims of exactly-once event delivery or physical power-loss testing.

See:

* `SPEC.md` — functional and technical requirements;
* `ARCHITECTURE.md` — architecture decisions and limitations;
* `TASKS.md` — loop and verification state;
* `AGENTS.md` — engineering rules.

## Prerequisites

* Go 1.27.1 (the version declared in `go.mod` and `dockerfile`);
* a Compose-compatible runtime, such as Docker Compose or Podman Compose;
* the `migrate` CLI for the commands in `Makefile`;
* `curl` and `jq` for the examples below.

Docker is not the protocol requirement: use the compatible Compose provider
already installed in the environment. For example, replace `docker compose`
below with `podman compose` where that is the available provider. Do not treat
a skipped conditional integration test as evidence of a working dependency.

## Local bootstrap

Copy the safe local configuration, then start the infrastructure:

```bash
cp .env.example .env
docker compose up -d postgres localstack keycloak
```

The Compose bootstrap imports the `wagering` Keycloak realm and creates:

* `wager-transactions.fifo`;
* `wager-transactions-dlq.fifo`, with the main queue redrive policy;
* `wager-events.fifo`.

For a host-side migration command, use a host-reachable PostgreSQL URL rather
than the Compose-network hostname in `.env.example`:

```bash
export DATABASE_URL='postgres://wagering:wagering@localhost:5432/wagering?sslmode=disable'
make migrate-up
```

Apply migrations before starting the application against an empty database.
The versioned migrations are reversible with one controlled step at a time:

```bash
make migrate-down
make migrate-up
```

`000003_opening_transaction.down.sql` intentionally refuses to remove
`OPENING` support while opening transactions exist, because deleting their
financial origin would be unsafe. Perform any production rollback through an
explicit maintenance procedure; do not delete financial data to make a
migration reverse.

After migrations are applied, start the complete stack:

```bash
docker compose up --build -d app
curl -fsS http://localhost:8080/health/ready
```

Expected readiness is PostgreSQL and SQS both `UP`. Liveness and readiness are
separate endpoints; metrics do not replace either health check.

## Local OIDC identities and authenticated calls

The realm import is local-development-only. It provisions the following test
identities, each with password `dev-only-pass`:

| Identity | Role | Provider claim |
| --- | --- | --- |
| `wallet-admin` | `internal` | none |
| `provider-alpha` | `provider` | `provider-alpha` |
| `provider-beta` | `provider` | `provider-beta` |

The client is `wagering-api` with the example secret `change-me`. These are
safe local values from `.env.example`, not deployment credentials.

Obtain local tokens without printing them:

```bash
TOKEN_URL='http://localhost:8082/realms/wagering/protocol/openid-connect/token'
token() {
  curl -fsS -X POST "$TOKEN_URL" \
    -H 'Content-Type: application/x-www-form-urlencoded' \
    --data-urlencode 'grant_type=password' \
    --data-urlencode 'client_id=wagering-api' \
    --data-urlencode 'client_secret=change-me' \
    --data-urlencode "username=$1" \
    --data-urlencode 'password=dev-only-pass' | jq -er '.access_token'
}
INTERNAL_TOKEN="$(token wallet-admin)"
PROVIDER_TOKEN="$(token provider-alpha)"
```

Open a wallet through the internal identity:

```bash
curl -fsS -X POST http://localhost:8080/wallets \
  -H "Authorization: Bearer $INTERNAL_TOKEN" \
  -H 'Content-Type: application/json' \
  --data '{"playerId":"player-001","initialBalance":{"amount":"100.00","currency":"BRL"}}'
```

Use the returned `id` as `WALLET_ID`, then submit an authenticated provider
operation. The request body provider must match the token claim.

```bash
curl -fsS -X POST http://localhost:8080/wagering/transactions \
  -H "Authorization: Bearer $PROVIDER_TOKEN" \
  -H 'Idempotency-Key: provider-alpha:bet-001' \
  -H 'Content-Type: application/json' \
  --data '{"providerId":"provider-alpha","externalTransactionId":"bet-001","playerId":"player-001","walletId":"'"$WALLET_ID"'","roundId":"round-001","gameId":"game-001","kind":"BET","money":{"amount":"25.00","currency":"BRL"}}'
```

Repeat the same request with the same `Idempotency-Key` to receive the
persisted result with `idempotentReplay: true`; it does not apply a second
movement. Business routes require a real Keycloak token. Opening wallets is
internal-only, while provider-scoped operations are restricted to the
authenticated provider.

## Operations and diagnostics

```bash
curl -fsS http://localhost:8080/health/live
curl -fsS http://localhost:8080/health/ready
curl -fsS http://localhost:8080/metrics
```

`X-Correlation-ID` is generated when absent. An external value is preserved
only if it is a bounded safe ASCII identifier; invalid input is replaced.
Application logs are JSON records with applicable correlation, message,
transaction, wallet and provider identifiers. Logs and metric labels exclude
credentials, full financial payloads, and business IDs. Metrics are
process-local and reset on restart; they are diagnostic rather than a
cluster-wide financial store. `wager_sqs_redrive_candidate_total` counts a
message that exhausted the application receive budget, not a broker DLQ entry
observed by the application.

## Verification

The standard quality gates are:

```bash
gofmt -l .
go test ./...
go test -race -count=1 ./...
go vet ./...
git diff --check
```

For the real integration matrix from a host, start the Compose dependencies
and use host endpoints explicitly. Conditional tests are enabled deliberately:

```bash
DATABASE_URL='postgres://wagering:wagering@localhost:5432/wagering?sslmode=disable' \
AWS_ENDPOINT_URL='http://localhost:4566' \
AWS_REGION='us-east-1' AWS_ACCESS_KEY_ID='test' AWS_SECRET_ACCESS_KEY='test' \
OIDC_ISSUER='http://localhost:8082/realms/wagering' \
OIDC_JWKS_URL='http://localhost:8082/realms/wagering/protocol/openid-connect/certs' \
OAUTH_AUDIENCE='wagering-api' OAUTH_CLIENT_ID='wagering-api' \
OAUTH_CLIENT_SECRET='change-me' \
KEYCLOAK_TOKEN_URL='http://localhost:8082/realms/wagering/protocol/openid-connect/token' \
KEYCLOAK_TEST_USERNAME='provider-alpha' KEYCLOAK_TEST_PASSWORD='dev-only-pass' \
RUN_KEYCLOAK_INTEGRATION=1 RUN_OUTBOX_PRODUCTION_INTEGRATION=1 \
RUN_OUTBOX_FX_INTEGRATION=1 \
go test -count=1 -v \
  ./internal/application/financial ./internal/application/query \
  ./internal/application/outbox ./internal/composition \
  ./internal/infrastructure/keycloak ./internal/infrastructure/postgres \
  ./internal/transport/http ./internal/transport/messaging
```

That command exercises PostgreSQL transactions and constraints, Keycloak
verification, SQS FIFO/DLQ, HTTP/SQS equivalence, inbox/outbox recovery,
pending references, multiple pools/processes, Fx lifecycle, and the controlled
failure scenarios. The Loop 9 ordering integrations use isolated PostgreSQL
schemas and FIFO queues, so aggregate execution does not depend on unrelated
rows or messages.

Focused examples for required adversarial scenarios are:

```bash
DATABASE_URL="$DATABASE_URL" go test -count=1 -run 'TestConcurrentBetsAcrossThreeOSProcesses|TestPersistentIdempotencyAcrossReplayRestartAndInstances' -v ./internal/application/financial
DATABASE_URL="$DATABASE_URL" AWS_ENDPOINT_URL='http://localhost:4566' go test -count=1 -run 'TestLoop11' -v ./internal/application/financial ./internal/application/outbox ./internal/transport/messaging
```

## Delivery boundaries

The outbox guarantees at-least-once publication after database commit. An
ambiguous send can republish the same immutable payload with the same stable
`eventId`; downstream consumers must deduplicate it. The controlled failure
tests use real PostgreSQL/LocalStack and child-process `SIGKILL` at selected
boundaries. They do not claim host power-loss or container-kill durability.
LocalStack validates broker behavior but does not prove AWS IAM enforcement;
deployment IAM remains an operational responsibility. No Loop 12 validation
introduces a publisher beyond Loop 9 or changes financial production code.

## License

This project is provided exclusively for technical evaluation, recruitment,
interview, and portfolio review purposes.

Production, commercial, redistribution, and derivative use is not authorized
without prior written permission from the copyright holder. See [LICENSE](LICENSE).
