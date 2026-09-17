# AGENTS.md

## Project

Backend challenge for distributed wagering transaction processing.

The system is a financial backend. Correctness, consistency, idempotency and failure recovery have priority over implementation speed or abstraction complexity.

## Engineering Principles

1. Preserve financial correctness above all other concerns.
2. Prefer explicit and verifiable behavior over clever abstractions.
3. Keep domain logic independent from HTTP, SQS, PostgreSQL, Keycloak and Uber Fx.
4. Infrastructure adapters must not contain business rules.
5. Financial invariants must be enforced both by application logic and PostgreSQL constraints where applicable.
6. Do not introduce dependencies unless they provide clear value.
7. Do not implement speculative features outside the challenge requirements.
8. Keep changes small and independently verifiable.

## Financial Rules

Never:

* use `float32` or `float64` for monetary values;
* silently round invalid monetary input;
* mutate or delete ledger entries;
* update wallet balance outside the financial database transaction;
* rely on in-memory state for idempotency;
* rely on SQS FIFO deduplication as the financial idempotency mechanism;
* use process-local locks as the financial concurrency mechanism;
* publish an integration event before the originating database transaction commits;
* allow a wallet balance to become negative;
* allow a processed financial transaction to be applied twice;
* allow two successful reversals of the same applicable type;
* bypass domain validation from an adapter.

## Money

Money must:

* use exact arithmetic;
* carry both amount and currency;
* use fixed two-decimal external representation;
* reject negative external financial inputs;
* reject scientific notation;
* reject excessive scale;
* reject invalid numeric representations;
* reject arithmetic between incompatible currencies;
* detect integer overflow when `int64` is used.

The external representation is:

```json
{
  "amount": "25.00",
  "currency": "BRL"
}
```

## Wallet

Wallet is the financial aggregate root.

Balance mutations must be performed atomically with their corresponding ledger entry and transaction state.

Wallet concurrency must be coordinated at wallet scope.

Independent wallets must be able to progress concurrently.

## Ledger

The ledger is append-only.

Every effective balance mutation must produce exactly one corresponding ledger entry.

Ledger entries are immutable.

Corrections must create new financial entries instead of modifying previous entries.

## Idempotency

Idempotency must survive process restarts.

The application must distinguish:

1. same idempotency key + same business payload;
2. same idempotency key + different business payload;
3. same external transaction identity + different idempotency key.

A successful replay must return the persisted result of the original processing.

Do not reconstruct the replay response from the wallet's current balance.

## Transactions

Financial changes must use explicit PostgreSQL transactions.

The transaction boundary must be visible in the repository/application implementation.

Where applicable, the following must commit atomically:

* wager transaction state;
* wallet balance;
* ledger entry;
* inbox completion;
* outbox event creation.

## HTTP and SQS

HTTP and SQS must use the same application use case for financial processing.

Transport-specific code must translate input into application commands and must not duplicate financial rules.

## Inbox

SQS message processing must use durable inbox records.

Inbox uniqueness must be enforced by PostgreSQL.

A duplicate message must not cause the financial operation to execute again.

## Outbox

Integration events must be created transactionally with the state they describe.

Outbox publication happens asynchronously after commit.

The outbox worker must support:

* multiple workers/instances;
* record claiming;
* retries;
* backoff;
* recovery of abandoned work;
* stable event IDs.

## Concurrency

The implementation must work with multiple independent application processes.

The required scenario is:

* wallet balance: `100.00 BRL`;
* two different `BET` operations;
* both amount `80.00 BRL`;
* submitted concurrently.

Expected result:

* exactly one transaction is `PROCESSED`;
* exactly one transaction is rejected for insufficient balance;
* final balance is `20.00 BRL`;
* exactly one debit exists in the ledger.

The implementation must not depend on Go process memory to guarantee this result.

## Authentication and Authorization

Business endpoints require real OAuth 2.0/OIDC authentication.

The authenticated identity determines the authorized `providerId`.

A provider must not access another provider's transactions, including replays.

Internal wallet-opening operations must not be exposed as provider operations.

Never implement password storage or custom token issuance.

## Uber Fx

Use Uber Fx for application composition and lifecycle management.

Use:

* `fx.Module`;
* `fx.Provide`;
* `fx.Invoke`;
* constructors;
* `fx.Lifecycle`.

The domain must not import Uber Fx.

## Context and Errors

All I/O operations must receive `context.Context`.

Respect cancellation and timeouts.

Business errors must be typed or classifiable using `errors.Is` / `errors.As`.

Do not use `panic` for business validation failures.

## Testing

Tests must verify behavior, not implementation details.

Required verification includes:

* unit tests;
* PostgreSQL integration tests;
* SQS integration tests;
* Keycloak authentication tests;
* concurrency tests;
* duplicate delivery;
* HTTP/SQS equivalence;
* outbox concurrency;
* retry/recovery;
* pending references;
* application restart;
* `go test -race`.

Do not replace PostgreSQL, SQS and the IdP entirely with mocks in integration tests.

## Development Workflow

Work in small vertical slices.

For each slice:

1. inspect the existing specification;
2. identify the relevant invariant;
3. implement the smallest coherent change;
4. add or update verification;
5. run formatting and relevant tests;
6. inspect failures;
7. fix the root cause;
8. update documentation if an architectural decision changed;
9. only then move to the next slice.

Do not implement unrelated improvements while working on a slice.

## Definition of Done

A feature is not considered complete merely because the code compiles.

A slice is complete when:

* implementation exists;
* relevant invariants are enforced;
* relevant tests exist;
* tests pass;
* race-sensitive code has been considered;
* documentation is updated where necessary;
* no known requirement is silently ignored.

## Agent Behavior

Before making a significant architectural change:

* inspect `SPEC.md`;
* inspect `ARCHITECTURE.md`;
* inspect `TASKS.md`;
* explain the intended change briefly;
* verify that it does not violate an existing decision.

If requirements conflict, do not silently choose one. Record the conflict in `TASKS.md` or `ARCHITECTURE.md` and resolve it explicitly.

Do not rewrite working code merely for stylistic preference.

Do not add abstractions without a concrete use case.

Do not claim a guarantee is implemented unless there is a test or a database constraint that demonstrates it.
