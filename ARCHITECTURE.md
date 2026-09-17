# Architecture

## 1. Overview

This project implements a distributed wagering transaction processor in Go.

The application is designed as a modular monolith that can run as multiple independent instances.

The primary architectural goal is financial correctness under:

* concurrent processing;
* duplicate delivery;
* process interruption;
* database failures;
* message broker redelivery;
* asynchronous event publication.

The system separates:

```text
Domain
Application
Infrastructure
Transport
Composition
```

The domain does not depend on HTTP, SQS, PostgreSQL, Keycloak or Uber Fx.

---

# 2. Architectural Goals

The architecture prioritizes:

1. Financial correctness.
2. Persistent idempotency.
3. Database-enforced invariants.
4. Safe concurrency across independent processes.
5. Atomic financial state changes.
6. Durable asynchronous processing.
7. Recoverability after failures.
8. Clear separation between domain and infrastructure.
9. Reproducibility in a local Docker environment.

The architecture intentionally avoids unnecessary distributed services.

Multiple application instances provide the distributed execution model required by the challenge.

---

# 3. High-Level Architecture

```text
                         ┌──────────────────┐
                         │     Keycloak     │
                         │     OIDC IdP     │
                         └────────┬─────────┘
                                  │
                                  ▼
┌─────────────┐            ┌───────────────┐
│ HTTP Client │───────────►│ HTTP Adapter  │
└─────────────┘            └───────┬───────┘
                                   │
                                   ▼
                            ┌──────────────┐
                            │ Application   │
                            │ Use Cases     │
                            └──────┬───────┘
                                   │
                     ┌─────────────┼─────────────┐
                     │             │             │
                     ▼             ▼             ▼
                 PostgreSQL      Domain       Outbox
                     ▲                           │
                     │                           ▼
                     │                      Outbox Worker
                     │                           │
                     │                           ▼
                     │                          SQS
                     │                           ▲
                     │                           │
                     │                      SQS Consumer
                     │                           │
                     └──────── Inbox ────────────┘
```

HTTP and SQS converge on the same application-level financial processing use case.

Transport adapters do not duplicate financial business rules.

---

# 4. Package Structure

Proposed structure:

```text
cmd/
  api/

internal/
  domain/
    money/
    wallet/
    wager/
    ledger/

  application/
    wallet/
    wager/

  infrastructure/
    postgres/
    sqs/
    keycloak/
    config/

  transport/
    http/
    messaging/

  fx/

migrations/

tests/
  integration/
  concurrency/
  recovery/
```

The exact package organization may evolve if implementation evidence demonstrates a better boundary.

Changes must preserve domain independence.

---

# 5. Domain Layer

The domain contains:

* entities;
* value objects;
* domain errors;
* state transitions;
* financial invariants;
* business rules.

The domain must not import:

* Uber Fx;
* HTTP packages;
* SQS SDK;
* PostgreSQL drivers;
* Keycloak libraries.

## Domain objects

Primary domain concepts:

```text
Money
Wallet
WagerTransaction
WalletLedgerEntry
```

Creation and rehydration must be separate concepts.

Rehydrating persisted state must not reapply financial operations or emit events.

---

# 6. Money Representation

## Decision

Use `int64` representing the smallest monetary unit.

For the current challenge:

```text
1 BRL = 100 cents
```

Example:

```text
"25.00" BRL -> 2500
```

The domain type still carries currency.

External serialization remains:

```json
{
  "amount": "25.00",
  "currency": "BRL"
}
```

## Rationale

`int64` provides exact deterministic arithmetic for fixed two-decimal monetary values without floating-point errors.

It also avoids introducing a decimal library unless future requirements require precision beyond the challenge's fixed scale.

## Required protections

Parsing, addition, subtraction and negation must detect overflow.

---

# 7. Persistence

## Decision

Use PostgreSQL with `pgx` and explicit SQL.

## Rationale

The challenge explicitly requires financial invariants, transactions, locks and constraints to remain visible and verifiable.

Explicit SQL makes:

* transaction boundaries;
* row locks;
* uniqueness constraints;
* check constraints;
* update conditions

easy to inspect.

Repository abstractions should remain focused on persistence operations rather than hiding important transaction semantics.

---

# 8. Financial Transaction Boundary

A financial operation must be committed atomically.

For a normal successful operation, the database transaction may contain:

```text
BEGIN

lock wallet
        ↓
validate transaction
        ↓
change wallet balance
        ↓
create wager transaction state
        ↓
create ledger entry
        ↓
create outbox events

COMMIT
```

No external event publication occurs before `COMMIT`.

The transaction boundary must be explicit in code.

---

# 9. Concurrency Strategy

## Decision

Use PostgreSQL row-level locking at wallet scope.

The expected primary mechanism is:

```sql
SELECT ...
FROM wallets
WHERE id = $1
FOR UPDATE;
```

The lock is acquired inside the same PostgreSQL transaction that performs the financial mutation.

## Rationale

The wallet is the financial aggregate root and therefore the natural coordination boundary.

Row-level locking:

* coordinates independent application processes;
* does not depend on local Go memory;
* prevents lost updates;
* serializes operations for the same wallet;
* allows different wallets to proceed concurrently.

Global application locks are prohibited.

---

# 10. Wallet Balance Invariant

A debit is allowed only when:

```text
balance >= debit
```

The database must participate in protecting this invariant.

Application validation alone is insufficient because multiple independent processes may execute concurrently.

The implementation must use both:

```text
application/domain validation
+
database transaction/constraint protection
```

to prevent negative balances.

---

# 11. Idempotency Strategy

Idempotency is persistent.

The database is the source of truth.

The implementation must persist:

* idempotency key;
* business identity;
* payload hash;
* processing state;
* persisted result information.

Database uniqueness must protect:

```text
(providerId, externalTransactionId)
```

and the applicable idempotency identity.

The system must distinguish:

```text
same key + same payload
same key + different payload
same transaction identity + different key
```

A replay must return the original persisted result.

The original balance returned by a replay is the balance observed when the transaction was processed, not the wallet's current balance.

---

# 12. Payload Hashing

Use canonical JSON for deterministic payload hashing.

Business fields are included.

Transport-specific metadata is excluded.

The idempotency key is excluded.

The same canonicalization implementation/rules must be shared between HTTP and SQS.

The algorithm must be documented and covered by tests.

The current canonical representation is JSON produced from the ordered business
fields `externalId`, `providerId`, `walletId`, `playerId`, `gameId`, `roundId`,
`referenceExternalId` (when present), `type` and the exact `{amount,currency}`
money value. The internal request ID, idempotency key, caller-provided hash and
transport metadata are excluded. The payload hash is the lowercase hexadecimal
SHA-256 digest of that canonical JSON.

---

# 13. Ledger

The ledger is append-only.

A ledger entry is never updated or deleted.

Database protections must prevent mutation.

The unique relationship:

```text
(walletId, transactionId)
```

prevents duplicate financial ledger entries.

The wallet balance and ledger entry are committed in the same database transaction.

The ledger is the authoritative audit trail used by reconciliation.

---

# 14. Transaction State Machine

```text
             ┌─────────────────────┐
             │       PENDING       │
             └──────────┬──────────┘
                        │
                processing required
                        │
             ┌──────────▼──────────┐
             │     PROCESSED       │
             └─────────────────────┘

             ┌─────────────────────┐
             │       PENDING       │
             └──────────┬──────────┘
                        │
                 reference missing
                        │
             ┌──────────▼──────────┐
             │ PENDING_REFERENCE   │
             └──────────┬──────────┘
                        │
                 reference resolved
                        │
                        ▼
                   processing
                        │
                ┌───────┴────────┐
                ▼                ▼
           PROCESSED          REJECTED


PENDING / processing failure
        │
        ▼
FAILED
```

Terminal states:

```text
PROCESSED
REJECTED
FAILED
```

Terminal transactions cannot transition again.

---

# 15. Reversals

Reversals are resolved through:

```text
(providerId, referenceExternalTransactionId)
```

Reference resolution must validate:

* provider;
* player;
* wallet;
* currency;
* round;
* original transaction state;
* original transaction type;
* reversal amount.

Duplicate reversals are prevented through database constraints and application validation.

---

# 16. Pending References

A missing reference is not treated as an infrastructure failure.

The transaction is durably persisted as:

```text
PENDING_REFERENCE
```

A worker periodically retries pending references.

The worker uses exponential backoff and a configurable retry limit or TTL.

Pending work survives application restarts.

---

# 17. Inbox Pattern

The SQS consumer uses a durable inbox.

The inbox provides application-level deduplication in addition to any SQS FIFO deduplication.

Identity:

```text
(consumerName, messageId)
```

The inbox record and financial processing must share the same database transaction when the message performs a financial operation.

A message is deleted from SQS only after the durable transaction commits.

---

# 18. Transactional Outbox

Integration events are persisted in the same PostgreSQL transaction as the state changes that caused them.

The outbox therefore closes the failure window between:

```text
database commit
```

and:

```text
event publication
```

A worker publishes pending events asynchronously.

Multiple workers may operate concurrently.

Workers must safely claim records so that two workers do not simultaneously own the same publication attempt.

Publication may be repeated after an ambiguous failure.

Therefore:

```text
eventId
```

must remain stable across retries and republication.

Consumers are expected to use event IDs for their own deduplication.

---

# 19. Event Model

Required events:

```text
WagerTransactionProcessed
WagerTransactionRejected
WalletBalanceChanged
WagerTransactionPendingReference
```

Events contain:

```text
eventId
eventType
aggregateId
correlationId
causationId
occurredAt
version
data
```

Event payloads are immutable snapshots.

Money is serialized as decimal strings.

Timestamps use UTC RFC 3339.

---

# 20. HTTP / SQS Equivalence

HTTP and SQS are different transport mechanisms for the same financial command.

The flow is:

```text
HTTP
  ↓
HTTP adapter
  ↓
Application command
  ↓
Financial use case
```

and:

```text
SQS
  ↓
Consumer
  ↓
Application command
  ↓
Financial use case
```

Business rules must not be implemented independently in the two paths.

This guarantees equivalent financial behavior regardless of transport.

---

# 21. Authentication

Use Keycloak as the local OAuth 2.0/OIDC provider.

The application validates tokens issued by the configured IdP.

The authenticated identity determines the authorized provider.

Provider authorization must be enforced before accessing provider-scoped transaction data.

The application does not:

* store user passwords;
* issue its own authentication tokens.

---

# 22. Authorization

Provider-scoped operations must always enforce:

```text
authenticated provider == requested provider
```

This applies to:

* transaction creation;
* transaction retrieval;
* transaction replay;
* external transaction lookup.

Internal wallet operations are not provider operations.

Authorization failures must not cause financial side effects.

---

# 23. Uber Fx

Uber Fx is responsible for application composition and lifecycle.

Expected dependency graph:

```text
Configuration
    ↓
Database / SQS / IdP clients
    ↓
Repositories
    ↓
Application services
    ↓
HTTP handlers / SQS consumers / workers
```

Use:

* `fx.Module`;
* `fx.Provide`;
* `fx.Invoke`;
* `fx.Lifecycle`.

The domain remains unaware of Fx.

---

# 24. Lifecycle and Shutdown

Application shutdown follows:

```text
SIGTERM
  ↓
stop accepting new work
  ↓
stop message polling
  ↓
finish or release in-flight work
  ↓
stop background workers
  ↓
close dependencies
  ↓
exit
```

Cancellation and deadlines must propagate through `context.Context`.

Workers must not be abandoned without a safe recovery mechanism.

---

# 25. Health Checks

## Liveness

```http
GET /health/live
```

Indicates that the process is alive.

## Readiness

```http
GET /health/ready
```

Checks required dependencies, primarily:

* PostgreSQL;
* SQS.

A dependency outage should make readiness fail without necessarily terminating the process.

---

# 26. Observability

Use structured JSON logging.

Important correlation identifiers:

```text
correlationId
messageId
transactionId
walletId
providerId
```

Logs must not contain complete sensitive financial payloads or credentials.

Metrics include:

```text
transaction status
idempotency duplicates
retry count
DLQ count
concurrency conflicts
outbox delay
processing latency
reconciliation divergence
```

---

# 27. Testing Strategy

Testing is organized into:

```text
unit
integration
concurrency
recovery
```

Unit tests verify domain behavior.

Integration tests verify real infrastructure behavior.

Concurrency tests verify multiple independent processes.

Recovery tests verify crash/restart behavior.

The goal is to prove system guarantees rather than merely increase code coverage.

---

# 28. Distributed Verification

The implementation must be exercised with at least three independent application processes.

Independent means:

* separate process;
* separate Go memory;
* separate database connection pool.

No correctness guarantee may depend on process-local memory.

---

# 29. Failure Model

The system assumes:

```text
at-least-once delivery
```

Therefore duplicate processing is expected.

The architecture must tolerate:

```text
duplicate HTTP request
duplicate SQS message
process crash before commit
process crash after commit
process crash after event publication
database temporary outage
SQS temporary outage
reference arriving late
```

The design treats retries and duplicates as normal operational conditions.

---

# 30. Local Infrastructure

Docker Compose provides:

```text
PostgreSQL
LocalStack
Keycloak
application dependencies
```

The exact versions are pinned or explicitly documented.

Infrastructure configuration must be reproducible from a clean checkout.

---

# 31. Security

Secrets are provided through environment variables.

Real secrets must never be committed.

`.env.example` contains only safe local example values.

Provider authorization must be enforced at the application boundary.

Logs must avoid credentials and sensitive payloads.

---

# 32. Architectural Trade-offs

The solution intentionally favors:

```text
explicit SQL
+
database transactions
+
row-level locks
+
persistent idempotency
+
inbox/outbox
```

over more abstract or distributed coordination mechanisms.

The challenge is evaluated primarily on correctness and recoverability.

Complexity should only be introduced where it protects a documented requirement.

---

# 33. Known Limitations

This section must be updated during implementation.

Every limitation should state:

* what is not implemented;
* why;
* impact;
* possible future improvement.

Do not hide incomplete requirements.

---

# 34. Architecture Decision Log

Architectural changes discovered during implementation should be recorded here.

Format:

```text
## ADR-NNN — Title

Status:
Date:

Context:

Decision:

Alternatives:

Consequences:
```

---

# 35. Current Status

Architecture status:

```text
PROPOSED
```

Implementation status must not be inferred from this document.

Only verified behavior should be described as implemented.
