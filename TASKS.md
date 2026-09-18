# Implementation Tasks

This file is the working state of the implementation.

The project follows an iterative engineering loop:

```text
SPEC
  ↓
PLAN
  ↓
IMPLEMENT
  ↓
VERIFY
  ↓
FIND GAPS
  ↓
FIX
  ↓
DOCUMENT
  ↓
NEXT LOOP
```

---

# Loop 0 — Project Foundation

## Goal

Establish the repository structure and local development environment.

### Tasks

* [x] Create project structure
* [x] Initialize Go module
* [x] Configure Go version
* [x] Create Docker Compose
* [x] Configure PostgreSQL
* [x] Configure LocalStack
* [x] Configure Keycloak
* [x] Create `.env.example`
* [x] Create Makefile
* [x] Create migration structure
* [x] Verify clean startup

### Verification

* [x] `docker compose up --build`
* [x] PostgreSQL reachable
* [x] LocalStack reachable
* [x] Keycloak reachable
* [x] application starts
* [x] application shuts down cleanly

---

# Loop 1 — Domain Foundation

## Goal

Implement exact financial domain behavior without infrastructure dependencies.

### Tasks

* [x] Money value object
* [x] Money parsing
* [x] Money serialization
* [x] Money arithmetic
* [x] Currency validation
* [x] Overflow protection
* [x] Wallet aggregate
* [x] Wallet creation
* [x] Wallet debit
* [x] Wallet credit
* [x] WagerTransaction
* [x] WalletLedgerEntry
* [x] Domain errors
* [x] Transaction state machine

### Verification

* [x] Money unit tests
* [x] Wallet invariant tests
* [x] Transaction transition tests
* [x] Operation rule tests
* [x] `go test ./...`

---

# Loop 2 — Database Foundation

## Goal

Persist the financial model with database-enforced invariants.

### Tasks

* [x] wallets table
* [x] wager_transactions table
* [x] wallet_ledger_entries table
* [x] inbox table
* [x] outbox table
* [x] constraints
* [x] unique indexes
* [x] immutable ledger protection
* [x] migrations
* [x] migration rollback
* [x] pgx repositories

### Verification

* [x] migrations apply
* [x] migrations rollback
* [x] uniqueness constraints verified
* [x] ledger mutation rejected
* [x] invalid balance rejected
* [x] integration tests pass

---

# Loop 3 — Financial Processing

## Goal

Implement all financial operations atomically.

### Tasks

* [x] wallet opening
* [x] BET
* [x] WIN
* [x] LOSS
* [x] REFUND
* [x] ROLLBACK
* [x] insufficient balance rejection
* [x] reference validation
* [x] reversal protection
* [x] atomic balance + transaction + ledger

### Verification

* [x] financial integration tests
* [x] ledger reconstruction
* [x] reconciliation
* [x] zero-value rules
* [x] reversal tests

> `FAILED` permanece reservado para falha permanente de infraestrutura registrada;
> no Loop 3, falhas de infraestrutura abortam a transação. O registro/recovery
> desse estado depende dos loops de messaging e recovery.

---

# Loop 4 — Idempotency

## Goal

Guarantee persistent idempotency across instances and restarts.

### Tasks

* [x] Idempotency-Key
* [x] canonical payload
* [x] payload hashing
* [x] duplicate detection
* [x] same key + same payload
* [x] same key + different payload
* [x] same transaction + different key
* [x] persisted original result
* [x] replay behavior

### Verification

* [x] 50 concurrent duplicate requests
* [x] restart application
* [x] replay after restart
* [x] cross-instance replay
* [x] conflict tests

> Registros anteriores ao Loop 4 que não possuem hash canônico compatível e
> resultado persistido não são reprocessados; a tentativa é recusada com
> `ErrReplayUnavailable` para preservar a segurança financeira.

---

# Loop 5 — Concurrency

## Goal

Prove correctness across independent application processes.

### Tasks

* [x] wallet row locking
* [x] transaction boundaries
* [x] concurrent debit protection
* [x] lost-update prevention
* [x] independent wallet parallelism

### Verification

* [x] two 80 BRL BETs against 100 BRL
* [x] exactly one successful debit
* [x] final balance 20 BRL
* [x] three independent instances
* [x] different wallets execute concurrently
* [x] `go test -race`

> A integração do Loop 5 inicia três executáveis independentes do binário de
> testes, cada um com memória e pool PostgreSQL próprios, e coordena somente
> uma barreira externa de início.

---

# Loop 6 — HTTP

## Goal

Expose the financial use cases through HTTP.

Current Status: COMPLETE — APPROVED

### Tasks

* [x] HTTP server
* [x] authentication middleware
* [x] authorization middleware
* [x] POST /wallets
* [x] GET /wallets/:walletId
* [x] GET /wallets/:walletId/ledger
* [x] POST /wagering/transactions
* [x] GET /wagering/transactions/:transactionId
* [x] GET /providers/:providerId/wagering/transactions/:externalTransactionId
* [x] POST /wallets/:walletId/reconciliation
* [x] GET /health/live
* [x] GET /health/ready
* [x] HTTP error contract

### Verification

* [x] authentication tests
* [x] authorization isolation tests
* [x] invalid input tests
* [x] replay tests
* [x] HTTP integration tests

### Post-Loop 6 Conformance (before Loop 7)

These unchecked items record primary-source contract reconciliation work. They
do not reopen the approved Loop 6 checkpoint and are not complete in this
document.

* [ ] POST-LOOP-6 HTTP CONTRACT CONFORMANCE
  * [x] reconcile request/response field names with the primary challenge
  * [x] confirm providerId is authoritative from authenticated identity
  * [x] reconcile the reconciliation response contract
  * [x] transition readiness to cover PostgreSQL and SQS in Loop 7

Readiness now includes PostgreSQL and SQS through the Loop 7 composition and
was exercised against the running local dependencies. AWS/IAM policy
enforcement remains a deployment verification responsibility.

---

# Loop 7 — SQS and Inbox

## Goal

Process financial operations through at-least-once messaging.

### Tasks

* [x] FIFO queue
* [x] DLQ
* [x] redrive configuration
* [x] SQS consumer
* [x] message envelope
* [x] message validation
* [x] inbox
* [x] inbox uniqueness
* [x] SQS → application command
* [x] retry
* [x] visibility timeout
* [x] graceful consumer shutdown
* [x] queue names and FIFO/DLQ contract
* [x] message envelope and command data fields
* [x] messageId and payload-hash identity
* [x] inbox transaction boundary and uniqueness
* [x] delete only after commit
* [x] business rejection acknowledgement
* [x] transient retry and backoff
* [x] malformed message handling
* [x] DLQ and attempt limits
* [x] SIGTERM stop polling and finish/release in-flight work
* [x] MessageGroupId and MessageDeduplicationId policy
* [x] HTTP/SQS application-use-case equivalence
* [x] broker credential configuration
* [x] minimum broker authorization policy documented
* [ ] AWS/IAM broker-policy enforcement verification (deployment responsibility; LocalStack does not prove it)
* [x] PostgreSQL and SQS readiness
* [x] real PostgreSQL + LocalStack integration execution

### Verification

* [x] duplicate message
* [x] message redelivery
* [x] commit before delete
* [x] business rejection acknowledgement
* [x] transient failure retry
* [x] DLQ behavior with real broker execution
* [x] restart recovery
* [x] visibility and malformed-message behavior
* [x] HTTP/SQS equivalence
* [x] Fx consumer survives `OnStart` context completion and stops polling
* [x] real readiness: PostgreSQL/SQS UP, each dependency DOWN, and recovery
* [x] duplicate delivery through two real SQS consumers and separate PostgreSQL pools

> Conditional tests remain conditional for the default local gate, but a
> skipped test is not integration evidence. The Integration Evidence Gate in
> `LOOPING.md` requires inspection of compatible installed runtimes and an
> explicit real PostgreSQL/LocalStack execution before this checkpoint can be
> marked verified. This Loop 7 correction run used the installed Podman and
> podman-compose environment; the real tests cover FIFO topology/redrive,
> consumer processing, durable inbox replay, delete-failure redelivery, DLQ,
> Fx lifecycle and PostgreSQL/SQS readiness recovery. LocalStack does not
> verify AWS IAM policy enforcement.

---

# Loop 8 — Pending References

## Goal

Recover reversals whose referenced transaction arrives later.

### Tasks

* [x] PENDING_REFERENCE
* [x] reference worker
* [x] exponential backoff
* [x] retry limit / TTL
* [x] reference resolution
* [x] reference expiration rejection
* [x] restartable exponential backoff
* [x] stable reference-not-found failureCode
* [x] pending/unsuccessful reference behavior

### Verification

* [x] reversal before reference
* [x] reference arrives later
* [x] application restart while pending
* [x] retry exhaustion
* [x] pending reference event
* [x] reference-at-exhaustion race with independent PostgreSQL pools
* [x] multiple pending-reference workers across independent PostgreSQL pools

> Loop 8 uses a ten-attempt default with one-second exponential backoff.
> `REFERENCE_REQUIRED` rejects an empty reference immediately; a non-empty
> missing reference becomes `PENDING_REFERENCE`. Pending references survive
> restart through PostgreSQL. Loop 9 remains responsible for publishing the
> resulting transactional outbox rows.

---

# Loop 9 — Transactional Outbox

## Goal

Guarantee durable event publication after financial commit.

### Tasks

* [x] outbox model
* [x] event constructors
* [x] immutable event payload
* [x] event IDs
* [x] outbox worker
* [x] record claiming
* [x] multiple publishers
* [x] retries
* [x] backoff
* [x] abandoned work recovery
* [x] event destination
* [x] atomic event creation with financial state
* [x] required event triggers and immutable snapshots
* [x] stable event IDs across republication
* [x] publication/confirmation failure windows
* [x] durable per-aggregate publication ordering

### Verification

* [x] outbox recovery
* [x] two publishers compete
* [x] publication retry
* [x] duplicate publication keeps event ID
* [x] all required events verified
* [x] same-aggregate ordering and independent-aggregate parallelism

> Loop 9 publishes versioned immutable event envelopes to the FIFO
> `wager-events.fifo` destination. PostgreSQL claims use a transaction-scoped
> lease and claim token; abandoned claims are recoverable by another worker.
> Publication ambiguity can result in repeated delivery with the same eventId.
> Process-crash injection remains Loop 11 scope.

---

# Loop 10 — Observability

## Goal

Make distributed processing diagnosable.

### Tasks

* [x] JSON logging
* [x] correlation ID
* [x] transaction ID
* [x] wallet ID
* [x] provider ID
* [x] message ID
* [x] processing metrics
* [x] duplicate metrics
* [x] retry metrics
* [x] receive-exhaustion/redrive-candidate metrics (broker DLQ insertion is
  not observed by the application)
* [x] outbox lag
* [x] reconciliation divergence

### Verification

* [x] logs contain required identifiers
* [x] sensitive data is not logged
* [x] metrics exposed
* [x] health checks verified
* [x] HTTP/SQS/pending-reference/outbox metric paths exercised without direct
  collector increments
* [x] correlation input safety and Fx JSON-only operational logging verified
* [x] receive-exhaustion is documented as a redrive candidate, not confirmed
  DLQ insertion
* [x] idempotency duplicates are distinct from SQS inbox redelivery and
  sequential idempotency conflicts are distinct from concurrency conflicts

> The targeted Loop 10 observability integrations were executed with real
> PostgreSQL, LocalStack, Keycloak and Fx. The Loop 9 ordering integrations
> use isolated PostgreSQL schemas and FIFO queues so concurrent packages cannot
> introduce unrelated outbox rows or consume the scenario's messages; the
> aggregate real-integration command passes with that test-harness correction.

---

# Loop 11 — Failure Engineering

## Goal

Attack the implementation and find correctness gaps.

### Scenarios

* [ ] duplicate HTTP
* [ ] duplicate SQS
* [ ] HTTP + SQS same operation
* [ ] concurrent wallet writes
* [ ] process crash before commit
* [ ] process crash after commit
* [ ] consumer crash before SQS delete
* [ ] outbox publisher crash
* [ ] PostgreSQL temporary outage
* [ ] SQS temporary outage
* [ ] pending reference during restart
* [ ] multiple instances
* [ ] replay after restart
* [ ] 50 duplicate requests produce one financial movement
* [ ] 100/80/80 concurrent-wallet scenario
* [ ] three independent application processes
* [ ] consumer crash after commit before delete
* [ ] two outbox publishers
* [ ] late reversal/reference scenarios
* [ ] HTTP/SQS same operation

### Verification

For every failure found:

```text
Failure
  ↓
Reproduction
  ↓
Root cause
  ↓
Fix
  ↓
Regression test
  ↓
Documentation
```

---

# Loop 12 — Final Quality Gate

### Code

* [ ] `gofmt`
* [ ] `go test ./...`
* [ ] `go test -race ./...`
* [ ] `go vet ./...`

### Infrastructure

* [ ] clean Docker Compose startup
* [ ] migrations reproducible
* [ ] Keycloak provisioning reproducible
* [ ] queues reproducible
* [ ] test identities reproducible

### Documentation

* [ ] README complete
* [ ] ARCHITECTURE complete
* [ ] `.env.example` complete
* [ ] limitations documented
* [ ] architecture decisions documented

### Final verification

* [ ] clean checkout
* [ ] full test suite
* [ ] concurrency scenario
* [ ] duplicate scenario
* [ ] recovery scenario
* [ ] reconciliation
* [ ] HTTP/SQS equivalence
* [ ] real PostgreSQL, SQS and IdP integration
* [ ] migrations up/down
* [ ] clean Docker Compose startup
* [ ] authenticated examples and test identities
* [ ] multi-instance and failure simulations
* [ ] gofmt, test, race and vet gates

---

# Current Loop

```text
Loop 9 — Transactional Outbox
```

# Current Status

```text
IMPLEMENTED — PENDING HUMAN REVIEW
```

# Rules

Do not mark a task complete because code exists.

A task is complete only after its relevant verification succeeds.

When a verification exposes a defect:

1. keep the task open;
2. document the failure;
3. fix the root cause;
4. add regression coverage;
5. rerun verification;
6. then mark the task complete.
