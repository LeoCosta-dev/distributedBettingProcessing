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

* [ ] wallets table
* [ ] wager_transactions table
* [ ] wallet_ledger_entries table
* [ ] inbox table
* [ ] outbox table
* [ ] constraints
* [ ] unique indexes
* [ ] immutable ledger protection
* [ ] migrations
* [ ] migration rollback
* [ ] pgx repositories

### Verification

* [ ] migrations apply
* [ ] migrations rollback
* [ ] uniqueness constraints verified
* [ ] ledger mutation rejected
* [ ] invalid balance rejected
* [ ] integration tests pass

---

# Loop 3 — Financial Processing

## Goal

Implement all financial operations atomically.

### Tasks

* [ ] wallet opening
* [ ] BET
* [ ] WIN
* [ ] LOSS
* [ ] REFUND
* [ ] ROLLBACK
* [ ] insufficient balance rejection
* [ ] reference validation
* [ ] reversal protection
* [ ] atomic balance + transaction + ledger

### Verification

* [ ] financial integration tests
* [ ] ledger reconstruction
* [ ] reconciliation
* [ ] zero-value rules
* [ ] reversal tests

---

# Loop 4 — Idempotency

## Goal

Guarantee persistent idempotency across instances and restarts.

### Tasks

* [ ] Idempotency-Key
* [ ] canonical payload
* [ ] payload hashing
* [ ] duplicate detection
* [ ] same key + same payload
* [ ] same key + different payload
* [ ] same transaction + different key
* [ ] persisted original result
* [ ] replay behavior

### Verification

* [ ] 50 concurrent duplicate requests
* [ ] restart application
* [ ] replay after restart
* [ ] cross-instance replay
* [ ] conflict tests

---

# Loop 5 — Concurrency

## Goal

Prove correctness across independent application processes.

### Tasks

* [ ] wallet row locking
* [ ] transaction boundaries
* [ ] concurrent debit protection
* [ ] lost-update prevention
* [ ] independent wallet parallelism

### Verification

* [ ] two 80 BRL BETs against 100 BRL
* [ ] exactly one successful debit
* [ ] final balance 20 BRL
* [ ] three independent instances
* [ ] different wallets execute concurrently
* [ ] `go test -race`

---

# Loop 6 — HTTP

## Goal

Expose the financial use cases through HTTP.

### Tasks

* [ ] HTTP server
* [ ] authentication middleware
* [ ] authorization middleware
* [ ] POST /wallets
* [ ] GET /wallets/:walletId
* [ ] GET /wallets/:walletId/ledger
* [ ] POST /wagering/transactions
* [ ] GET /wagering/transactions/:transactionId
* [ ] GET /providers/:providerId/wagering/transactions/:externalTransactionId
* [ ] POST /wallets/:walletId/reconciliation
* [ ] GET /health/live
* [ ] GET /health/ready
* [ ] HTTP error contract

### Verification

* [ ] authentication tests
* [ ] authorization isolation tests
* [ ] invalid input tests
* [ ] replay tests
* [ ] HTTP integration tests

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

### Verification

* [ ] duplicate message
* [ ] message redelivery
* [ ] commit before delete
* [ ] business rejection acknowledgement
* [ ] transient failure retry
* [ ] DLQ behavior
* [ ] restart recovery

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

---

# Current Loop

```text
Loop 0 — Project Foundation
```

# Current Status

```text
LOOP 0 COMPLETE
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
