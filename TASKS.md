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
  * [ ] transition readiness to cover PostgreSQL and SQS in Loop 7

The PostgreSQL-only readiness currently implemented for Loop 6 remains
intentionally pending until the real SQS dependency is integrated in Loop 7.

---

# Loop 7 — SQS and Inbox

## Goal

Process financial operations through at-least-once messaging.

### Tasks

* [ ] FIFO queue
* [ ] DLQ
* [ ] redrive configuration
* [ ] SQS consumer
* [ ] message envelope
* [ ] message validation
* [ ] inbox
* [ ] inbox uniqueness
* [ ] SQS → application command
* [ ] retry
* [ ] visibility timeout
* [ ] graceful consumer shutdown
* [ ] queue names and FIFO/DLQ contract
* [ ] message envelope and command data fields
* [ ] messageId and payload-hash identity
* [ ] inbox transaction boundary and uniqueness
* [ ] delete only after commit
* [ ] business rejection acknowledgement
* [ ] transient retry and backoff
* [ ] malformed message handling
* [ ] DLQ and attempt limits
* [ ] SIGTERM stop polling and finish/release in-flight work
* [ ] MessageGroupId and MessageDeduplicationId policy
* [ ] HTTP/SQS application-use-case equivalence
* [ ] broker credentials and policies
* [ ] PostgreSQL and SQS readiness
* [ ] real LocalStack/MiniStack integration

### Verification

* [ ] duplicate message
* [ ] message redelivery
* [ ] commit before delete
* [ ] business rejection acknowledgement
* [ ] transient failure retry
* [ ] DLQ behavior
* [ ] restart recovery
* [ ] visibility and malformed-message behavior
* [ ] HTTP/SQS equivalence

---

# Loop 8 — Pending References

## Goal

Recover reversals whose referenced transaction arrives later.

### Tasks

* [ ] PENDING_REFERENCE
* [ ] reference worker
* [ ] exponential backoff
* [ ] retry limit / TTL
* [ ] reference resolution
* [ ] reference expiration rejection
* [ ] restartable exponential backoff
* [ ] stable reference-not-found failureCode
* [ ] pending/unsuccessful reference behavior

### Verification

* [ ] reversal before reference
* [ ] reference arrives later
* [ ] application restart while pending
* [ ] retry exhaustion
* [ ] pending reference event

---

# Loop 9 — Transactional Outbox

## Goal

Guarantee durable event publication after financial commit.

### Tasks

* [ ] outbox model
* [ ] event constructors
* [ ] immutable event payload
* [ ] event IDs
* [ ] outbox worker
* [ ] record claiming
* [ ] multiple publishers
* [ ] retries
* [ ] backoff
* [ ] abandoned work recovery
* [ ] event destination
* [ ] atomic event creation with financial state
* [ ] required event triggers and immutable snapshots
* [ ] stable event IDs across republication
* [ ] publication/confirmation failure windows

### Verification

* [ ] commit then process crash
* [ ] outbox recovery
* [ ] two publishers compete
* [ ] publication retry
* [ ] duplicate publication keeps event ID
* [ ] all required events verified

---

# Loop 10 — Observability

## Goal

Make distributed processing diagnosable.

### Tasks

* [ ] JSON logging
* [ ] correlation ID
* [ ] transaction ID
* [ ] wallet ID
* [ ] provider ID
* [ ] message ID
* [ ] processing metrics
* [ ] duplicate metrics
* [ ] retry metrics
* [ ] DLQ metrics
* [ ] outbox lag
* [ ] reconciliation divergence

### Verification

* [ ] logs contain required identifiers
* [ ] sensitive data is not logged
* [ ] metrics exposed
* [ ] health checks verified

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
Loop 6 — HTTP
```

# Current Status

```text
COMPLETE — APPROVED
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
