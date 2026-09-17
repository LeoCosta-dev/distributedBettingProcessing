# Backend Challenge — Distributed Wagering Processing

## 0. Normative Scope and Restoration Provenance

This document is the normative functional and technical specification for the
distributed wagering transaction processor.

This version restores the existing content of SPEC.md and consolidates
decisions that are explicit in the project documentation. The source precedence
used for the restoration is:

1. the content that existed in SPEC.md;
2. explicit decisions in ARCHITECTURE.md;
3. permanent requirements in AGENTS.md;
4. requirements and acceptance criteria in TASKS.md;
5. behavior already implemented and verified in Loops 0–5, only as auxiliary
   evidence for decisions already determined by the sources above.

The sections before Open Specification Gaps are restored normative
requirements. A gap is not a requirement: it records a decision that remains
necessary but is not selected by this document.

The specification does not infer implementation completion from this document.
Loop status and human-review state remain controlled by TASKS.md and the
looping process.

---

## 1. Purpose

Implement a Go backend service capable of processing financial wagering
operations from HTTP and SQS in a distributed environment.

The system must preserve financial correctness when multiple application
instances process operations concurrently and when failures occur between
processing stages.

The system must support:

* wallet creation;
* wagering transactions;
* persistent idempotency;
* concurrent wallet operations;
* asynchronous SQS processing;
* transactional inbox;
* transactional outbox;
* pending transaction references;
* reversals;
* reconciliation;
* OAuth 2.0/OIDC authentication and authorization;
* recovery after process interruption;
* observable health and processing state.

---

## 2. Non-Negotiable Invariants

These invariants must always hold.

### 2.1 Money

* Monetary values must never use float32 or float64.
* External monetary values use strings with exactly two decimal places.
* Currency uses ISO 4217 codes.
* External financial inputs cannot be negative.
* Scientific notation is rejected.
* Excessive decimal scale is rejected.
* Invalid values are rejected instead of silently rounded.
* Arithmetic between different currencies is rejected.
* Integer overflow must be detected if int64 is used.
* Internal calculations may temporarily contain negative values.
* Wallet balances may never become negative.

### 2.2 Wallet and ledger

* A wallet is the financial aggregate root.
* Every effective financial balance mutation has exactly one corresponding
  ledger entry committed atomically with the balance update and transaction
  state.
* Ledger entries are append-only and immutable.
* Corrections create new financial entries; previous ledger entries are never
  mutated or deleted.
* Wallet concurrency is coordinated at wallet scope.
* Independent wallets must be able to progress concurrently.

### 2.3 Idempotency and delivery

* Idempotency must survive process restarts and must not rely on process-local
  memory or SQS FIFO deduplication.
* The system distinguishes:
  1. the same idempotency key with the same business payload;
  2. the same idempotency key with a different business payload;
  3. the same external transaction identity with a different idempotency key.
* A successful replay returns the persisted result of the original processing.
* A replay never reconstructs its response from the wallet's current balance.
* At-least-once delivery, duplicate messages and duplicate requests are normal
  operating conditions.

### 2.4 Atomicity

Financial changes use explicit PostgreSQL transactions. The transaction
boundary must be visible in the application or repository implementation.

Where applicable, the following must commit atomically:

* wager transaction state;
* wallet balance;
* ledger entry;
* inbox completion;
* outbox event creation.

Financial invariants that PostgreSQL can enforce must also be protected by
database constraints, uniqueness, foreign keys, indexes or triggers. This
includes non-negative wallet balances, identity uniqueness, currency
relationships and ledger
immutability.

No integration event is published before the originating transaction commits.

### 2.5 Separation of responsibilities

HTTP and SQS translate transport input into the same application financial
command and use case. Transport adapters must not duplicate financial rules or
bypass domain validation.

---

## 3. Money Contract

The external representation is:

~~~json
{
  "amount": "25.00",
  "currency": "BRL"
}
~~~

Money carries both an exact amount and its currency. The selected internal
representation is int64 in the smallest monetary unit. For the current
challenge:

~~~text
1 BRL = 100 cents
"25.00" BRL = 2500
~~~

The domain must support:

* construction from a decimal string;
* zero by currency;
* addition;
* subtraction;
* negation;
* comparison;
* serialization and deserialization using the external representation.

Parsing, addition, subtraction and negation must detect integer overflow.
External parsing must reject negative values, scientific notation, invalid
numeric representations and excessive scale. Invalid input must not be
silently rounded.

Arithmetic between incompatible currencies must fail. The main scenarios may
operate only with BRL, provided the domain continues to carry currency and
tests cover incompatible-currency behavior.

Internal arithmetic may temporarily represent a negative intermediate value,
but a wallet balance must never be negative.

---

## 4. Wallet

A wallet is uniquely identified by:

~~~text
(playerId, currency)
~~~

A wallet contains:

* id;
* playerId;
* currency;
* balance;
* version;
* createdAt;
* updatedAt.

The initial version is 1. The version increases only when the wallet balance
changes. A zero-value operation does not change the version.

Debit must verify that the amount is non-negative, has the wallet currency and
does not exceed the current balance. Credit must verify that the amount is
non-negative, has the wallet currency and does not overflow. A non-zero
successful balance change must update the wallet and create its ledger entry in
the same database transaction.

The wallet row is the coordination boundary for financial concurrency. A
solution must coordinate operations on the same wallet across independent
processes and must not use a process-local lock as the financial guarantee.

---

## 5. Wager Transactions

### 5.1 Types

The supported transaction types are:

~~~text
OPENING
BET
WIN
LOSS
REFUND
ROLLBACK
~~~

External operations are:

~~~text
BET
WIN
LOSS
REFUND
ROLLBACK
~~~

OPENING is reserved for internal wallet creation. An external request
containing OPENING must be rejected and must not create a provider
transaction.

### 5.2 Identity and fields

A wagering transaction contains, when applicable:

* internal transaction ID;
* external transaction ID;
* providerId;
* walletId;
* playerId;
* gameId;
* roundId;
* transaction type;
* exact amount and currency;
* state;
* idempotency key;
* canonical payload hash;
* persisted processing result;
* referenceExternalId for reversal operations.

The external transaction identity is (providerId, externalTransactionId)
and must be unique. The applicable idempotency identity is also unique in the
database. Transaction and wallet currencies must match.

The transaction player, wallet, provider and business identity must be
validated before a financial mutation is applied.

### 5.3 Operation rules

The supported operation names are defined above. The normative sources
explicitly establish the following financial rule for the required concurrency
scenario:

* BET is a debit.
* A debit is rejected for insufficient balance without changing the wallet or
  creating ledger movement.
* A valid business rejection is durably recorded as REJECTED and does not
  create an effective balance mutation.

The sources do not determine the exact amount constraints and balance effects
for WIN, LOSS, REFUND or ROLLBACK, which original transaction types each
reversal type may reference, or the reversal direction. Those decisions remain
Open Specification Gaps.

### 5.4 Transaction states

The states are:

~~~text
PENDING
PROCESSED
REJECTED
FAILED
PENDING_REFERENCE
~~~

The state machine permits:

~~~text
PENDING -> PROCESSED
PENDING -> REJECTED
PENDING -> FAILED
PENDING -> PENDING_REFERENCE
PENDING_REFERENCE -> PROCESSED
PENDING_REFERENCE -> REJECTED
~~~

PROCESSED, REJECTED and FAILED are terminal states. Terminal transactions
cannot transition again. PENDING_REFERENCE is durable, retryable work and
cannot transition directly to FAILED without an explicit failure policy.

FAILED is reserved for a permanently failed infrastructure processing result
that is durably recorded. In the financial-processing slice, an infrastructure
failure aborts the PostgreSQL transaction; recording and recovering this state
belongs to messaging and recovery behavior.

### 5.5 References and PENDING_REFERENCE

REFUND and ROLLBACK resolve their reference using:

~~~text
(providerId, referenceExternalId)
~~~

Reference validation must check:

* provider;
* player;
* wallet;
* currency;
* round;
* original transaction state;
* original transaction type;
* reversal amount.

A missing reference is not an infrastructure failure. The reversal is durably
stored as PENDING_REFERENCE, without changing the wallet or ledger, and a
pending-reference event is created transactionally. A later resolution either
processes the reversal or rejects it.

The system must not allow two successful reversals of the same applicable
type. The interaction and cardinality between distinct reversal types are not
determined; see Open Specification Gaps.

---

## 6. Wallet Opening

Wallet opening is an internal application operation. It creates a wallet with
the requested player, currency and non-negative opening balance, subject to the
(playerId, currency) uniqueness invariant.

For a positive opening balance, the operation creates an internal OPENING
transaction, a credit ledger entry and the corresponding events atomically with
wallet creation.

The internal opening operation must not be exposed as a provider wagering
operation. External callers cannot submit OPENING through the wagering
transaction flow.

---

## 7. Ledger

The ledger is the append-only authoritative audit trail for reconciliation.

Ledger entries are immutable and cannot be updated or deleted. Corrections
create new financial entries instead of modifying previous entries. The
relationship (walletId, transactionId) must prevent more than one corresponding
ledger entry for the same wallet transaction.

Every effective balance mutation creates exactly one corresponding ledger entry
committed atomically with the balance update and transaction state. The exact
ledger-entry schema and movement representation remain open; see Open
Specification Gaps.


---

## 8. Idempotency

### 8.1 Persistent identities

The database is the idempotency source of truth. It persists:

* the idempotency key;
* business identity;
* canonical payload hash;
* processing state;
* persisted result information.

The Idempotency-Key is required for external financial processing. Database
uniqueness protects at least:

~~~text
(providerId, externalTransactionId)
~~~

and the applicable idempotency identity, whose exact scope and fields are not
selected here; see Open Specification Gaps.

Concurrent attempts that race on the specified external transaction or
idempotency identities must resolve to the persisted winner or to a classifiable
idempotency conflict; a unique-constraint error must not result in a second
financial mutation.

### 8.2 Canonical payload and hash

The canonical payload is JSON produced from these ordered business fields:

~~~text
externalId
providerId
walletId
playerId
gameId
roundId
referenceExternalId (when present)
type
amount
~~~

amount is the exact {amount,currency} money value. The internal request ID,
idempotency key, caller-provided hash and transport metadata are excluded.

The payload hash is the lowercase hexadecimal SHA-256 digest of that canonical
JSON. The same canonicalization rules must be used by HTTP and SQS processing
before the application command is executed. The service calculates the hash;
the caller-provided hash is not authoritative.

### 8.3 Replay and conflicts

* Same idempotency key and same business payload: return the original persisted
  result without applying the operation again.
* Same idempotency key and different business payload: return a classifiable
  idempotency conflict without financial side effects.
* Same external transaction identity and different idempotency key: return a
  classifiable idempotency conflict without financial side effects.

The replay response uses the persisted result of the original processing and
must not re-execute the financial movement or reconstruct the response from the
wallet's current balance. Its exact persisted representation and the behavior
for each transaction state remain open; see Open Specification Gaps.

If an existing record has no compatible canonical hash or no valid persisted
result, it is not reprocessed. The attempt is refused as replay unavailable to
preserve financial correctness.

---

## 9. Financial Processing and PostgreSQL Transactions

A normal financial operation uses one explicit PostgreSQL transaction. Where
applicable, that transaction contains wallet coordination, validation, wager
transaction state, wallet balance, ledger entry, inbox completion and outbox
event creation. This specification does not select an order for those steps.

The wallet row lock is acquired inside the same transaction as the mutation.
Application/domain validation and database constraints must both protect the
non-negative-balance invariant.

All effects that compose the operation must remain inside the same PostgreSQL
transaction and become confirmed only when that transaction commits.

For a business rejection, the transaction record and rejection event are
persisted atomically, while the wallet and ledger remain unchanged. For a
missing reference, the transaction record and pending-reference event are
persisted atomically, while the wallet and ledger remain unchanged.

If the transaction does not commit, none of its financial state, ledger entry,
inbox completion or outbox records may be treated as committed. No external
event may be published before commit.

All I/O receives context.Context, and cancellation and timeouts must be
respected. Business errors must be typed or classifiable with errors.Is or
errors.As; business validation failures must not use panic.

---

## 10. HTTP Contract Known to This Specification

HTTP and SQS are transport adapters for the same application financial use
case. The following method/path inventory is explicitly known:

| Method | Path | Known purpose |
| --- | --- | --- |
| POST | /wallets | Wallet opening/creation operation |
| GET | /wallets/:walletId | Wallet retrieval |
| GET | /wallets/:walletId/ledger | Wallet ledger retrieval |
| POST | /wagering/transactions | Financial transaction processing |
| GET | /wagering/transactions/:transactionId | Transaction retrieval |
| GET | /providers/:providerId/wagering/transactions/:externalTransactionId | Provider-scoped external transaction lookup/replay |
| POST | /wallets/:walletId/reconciliation | Wallet reconciliation operation |
| GET | /health/live | Process liveness |
| GET | /health/ready | Dependency readiness |

The adapter translates a request into the application command and must not
reimplement transaction, reference, idempotency or authorization rules.

Business endpoints require real OAuth 2.0/OIDC authentication and provider
authorization. Provider-scoped transaction data, including replays, must be
isolated by provider before any data access or financial side effect.

This section records only the contract known from the sources. It intentionally
does not assign HTTP status codes, an error envelope, exact request/response
schemas, or an exact token claim; those remain open gaps below.

---

## 11. Authentication and Authorization

Keycloak is the configured local OAuth 2.0/OIDC identity provider. The
application validates tokens issued by the configured IdP and uses the
authenticated identity to determine the authorized providerId.

Authorization must be enforced before provider-scoped access to:

* transaction creation;
* transaction retrieval;
* transaction replay;
* external transaction lookup;
* any other provider-scoped wagering data.

An authenticated provider must not access another provider's transactions,
including replay responses. Internal wallet opening is not a provider
operation.

The system must not store user passwords and must not issue custom authentication
tokens in place of the IdP.

The exact claim, role, scope, audience and endpoint policy are intentionally not
selected here; see Open Specification Gaps.

---

## 12. SQS Processing

SQS processing assumes at-least-once delivery. The local infrastructure
provisions a FIFO wagering queue and a FIFO dead-letter queue with redrive
configuration. FIFO deduplication is not the financial idempotency mechanism.

The SQS consumer must:

* validate the message envelope;
* translate it into the same application financial command used by HTTP;
* use a durable inbox record;
* execute the financial command and inbox completion in the applicable database
  transaction;
* delete the SQS message only after that transaction commits;
* acknowledge/delete a durably committed business rejection;
* leave transient failures retryable through visibility timeout and redelivery;
* support graceful shutdown.

Duplicate delivery must not execute the financial operation twice. A message
redelivered after a commit but before SQS deletion must be recognized by the
durable inbox and/or financial idempotency records.

---

## 13. Inbox

Inbox records are durable PostgreSQL records. Their identity is:

~~~text
(consumerName, messageId)
~~~

The database enforces uniqueness for that identity. An inbox record contains,
when applicable, the information necessary to identify the message and its
processing state. The exact inbox schema, fields and interrupted-processing
representation remain open; see Open Specification Gaps.

Inbox completion and the financial operation caused by the message share the
same database transaction. A duplicate message must not cause the financial
operation to execute again, and an uncommitted transaction must not leave a
completed inbox record behind.

---

## 14. Transactional Outbox

Integration events are created in the same PostgreSQL transaction as the state
changes they describe. Outbox publication is asynchronous and starts only
after commit.

The outbox worker must support:

* multiple workers or application instances;
* safe record claiming;
* retries;
* backoff;
* recovery of abandoned work;
* stable event IDs across retries and republication.

Publication may be repeated after an ambiguous failure. Consumers are expected
to use the stable event ID for their own deduplication. An outbox worker must
not lose a committed event merely because a process stops between commit and
publication.

---

## 15. Event Model

The required event types are:

~~~text
WagerTransactionProcessed
WagerTransactionRejected
WalletBalanceChanged
WagerTransactionPendingReference
~~~

Events contain:

~~~text
eventId
eventType
aggregateId
correlationId
causationId
occurredAt
version
data
~~~

Event payloads are immutable snapshots of the state they describe. Money is
serialized as decimal strings, and timestamps use UTC RFC 3339. The exact
conditions for emitting each event, including WalletBalanceChanged, remain
open; see Open Specification Gaps.

---

## 16. Reconciliation

Reconciliation compares the persisted wallet balance with a reconstruction from
the wallet's immutable ledger. It must report whether the two are consistent and
must detect an inconsistent ledger rather than silently accepting it.

Reconciliation is not permission to mutate or delete ledger history. Any
correction must be represented by new financial entries and must preserve the
wallet, ledger and transaction atomicity rules.

The known HTTP entry point is:

~~~http
POST /wallets/:walletId/reconciliation
~~~

Its wire response and authorization details are not determined by the current
sources.

---

## 17. Concurrency

The implementation must work with multiple independent application processes.
PostgreSQL row-level locking at wallet scope is the primary coordination
mechanism. The wallet lock is acquired in the financial transaction, for
example:

~~~sql
SELECT ...
FROM wallets
WHERE id = $1
FOR UPDATE;
~~~

The implementation must not rely on Go process memory or process-local locks.
Operations on different wallets must not be blocked by a lock held for another
wallet.

The required scenario is:

* wallet balance: 100.00 BRL;
* two different BET operations;
* both amount 80.00 BRL;
* submitted concurrently.

The expected result is:

* exactly one transaction is PROCESSED;
* exactly one transaction is rejected for insufficient balance;
* final balance is 20.00 BRL;
* exactly one debit exists in the ledger, in addition to any opening entry.

Verification must include at least three independent application processes or
equivalent independent processes with separate Go memory and database
connection pools.

---

## 18. Recovery and Failure Model

The system assumes:

~~~text
at-least-once delivery
~~~

It must tolerate:

* duplicate HTTP requests;
* duplicate SQS messages;
* the same operation arriving through HTTP and SQS;
* process crash before database commit;
* process crash after database commit;
* consumer crash before SQS deletion;
* outbox publisher crash;
* temporary PostgreSQL outage;
* temporary SQS outage;
* a reference arriving late;
* pending work during application restart;
* multiple application instances;
* replay after restart.

The required failure-window behavior is:

* Before commit, the database transaction is rolled back and its financial
  mutation, ledger entry, inbox completion and outbox records are not visible
  as committed.
* After commit and before SQS deletion, redelivery is safe because the inbox
  and financial idempotency state are durable.
* After a financial commit and before event publication, the outbox record is
  recovered asynchronously.
* After an ambiguous event publication, republication keeps the same event ID.
* Pending references survive process restart and remain retryable until the
  configured pending policy resolves or rejects them.

Transient dependency failures must not create a partial financial mutation.
Permanent infrastructure failure may be recorded as FAILED according to the
failure policy; that policy is an open specification gap.

---

## 19. Lifecycle and Shutdown

Uber Fx manages application composition and lifecycle. Shutdown follows this
sequence:

~~~text
SIGTERM
  ↓
stop accepting new work
  ↓
stop message polling
  ↓
finish or safely release in-flight work
  ↓
stop background workers
  ↓
close dependencies
  ↓
exit
~~~

Cancellation and deadlines propagate through context.Context. Workers must not
be abandoned without a durable recovery mechanism.

The exact grace period and in-flight-work policy remain open.

---

## 20. Health and Observability

### 20.1 Health

~~~http
GET /health/live
GET /health/ready
~~~

Liveness indicates that the process is alive. Readiness checks required
dependencies, primarily PostgreSQL and SQS. A dependency outage makes
readiness fail without necessarily terminating the process.

The exact readiness wire contract and probe policy remain open.

### 20.2 Logs and metrics

Use structured JSON logging. Important correlation identifiers include:

~~~text
correlationId
messageId
transactionId
walletId
providerId
~~~

Logs must not contain complete sensitive financial payloads or credentials.

Metrics must cover, when applicable:

~~~text
transaction status
idempotency duplicates
retry count
DLQ count
concurrency conflicts
outbox delay
processing latency
reconciliation divergence
~~~

---

## 21. Technology and Architectural Requirements

The system is a modular monolith that can run as multiple independent
instances. It separates:

~~~text
Domain
Application
Infrastructure
Transport
Composition
~~~

The domain must not depend on HTTP, SQS, PostgreSQL, Keycloak or Uber Fx. It
contains entities, value objects, domain errors, state transitions, financial
invariants and business rules. Creation and rehydration are separate concepts;
rehydration must not reapply operations or emit events.

Use:

* Go;
* PostgreSQL;
* pgx and explicit SQL for persistence;
* AWS SQS, with LocalStack for local development;
* Keycloak as the local OIDC provider;
* Uber Fx for composition and lifecycle;
* Docker Compose for reproducible local infrastructure.

Uber Fx composition uses fx.Module, fx.Provide, fx.Invoke, constructors and
fx.Lifecycle. The domain does not import Uber Fx.

Explicit SQL must keep transaction boundaries, row locks, uniqueness, check
constraints and update conditions inspectable. PostgreSQL migrations must be
versioned and support the rollback behavior defined by the project.

Local infrastructure includes PostgreSQL, LocalStack SQS and Keycloak. Safe
example configuration may be committed, but real secrets must be supplied by
environment variables and must not be committed. A clean checkout must be
reproducible from Docker Compose, including queue and IdP provisioning.

The exact package organization may evolve if the domain boundaries and
invariants remain intact.

---

## 22. Testing, Quality Gates and Delivery Criteria

Tests verify behavior and invariants, not implementation details alone.

Required verification includes:

* unit tests for domain behavior;
* PostgreSQL integration tests;
* SQS integration tests;
* Keycloak authentication tests;
* HTTP/SQS equivalence tests;
* concurrency and multi-process tests;
* duplicate delivery tests;
* persistent idempotency and restart tests;
* outbox concurrency tests;
* retry and recovery tests;
* pending-reference tests;
* reconciliation tests;
* go test -race.

PostgreSQL, SQS and the IdP must not be entirely replaced by mocks in their
integration tests.

The required quality gates are:

~~~bash
gofmt
go test ./...
go test -race ./...
go vet ./...
git diff --check
~~~

The formatting command must act only on applicable Go files. Applicable
additional gates include migrations up/down, real PostgreSQL, LocalStack SQS,
Keycloak authentication and authorization, shutdown, multi-instance,
idempotency, recovery and clean Docker Compose startup tests.

A feature is complete only when implementation, invariant enforcement, tests,
passing relevant gates, race-sensitive review and required documentation are
present. Compilation alone is insufficient. A loop remains pending human
review until its evidence is reviewed and approved; no subsequent loop is
started automatically.

---

## Open Specification Gaps

This section is non-normative. Each item records a necessary decision that the
inspected sources do not determine sufficiently. No option below is selected.

### GAP-WAGER-001 — Per-operation financial semantics

* Decision absent: exact amount constraints and balance effects for WIN, LOSS,
  REFUND and ROLLBACK; which original transaction types each reversal type may
  reference; reversal direction; and zero-value behavior by operation type.
* Why necessary: the operation names and the general reference and reversal
  invariants do not uniquely define the financial effect of every operation.
* Sources inspected: the original truncated SPEC.md; AGENTS.md Financial
  Rules, Money and Concurrency; ARCHITECTURE.md sections 14–15; TASKS.md Loop
  3; Loop 3 implementation and tests as auxiliary evidence only.
* Possible options, not selected: define a complete type-by-type operation
  matrix, define only the reference/reversal matrix, or defer the remaining
  details to the HTTP/SQS contract.

### GAP-OPENING-001 — Zero-opening semantics

* Decision absent: whether opening a wallet with a zero balance creates an
  OPENING transaction, a ledger entry, corresponding events, or only wallet
  state.
* Why necessary: wallet creation and the financial audit trail need a
  deterministic relationship even when no balance movement occurs.
* Sources inspected: the original truncated SPEC.md; AGENTS.md Wallet and
  Ledger; ARCHITECTURE.md sections 8 and 15; TASKS.md Loop 3 zero-value
  verification; current implementation as auxiliary evidence only.
* Possible options, not selected: record an explicit zero OPENING or record
  only the wallet creation while reserving OPENING for an effective movement.

### GAP-REVERSAL-001 — Cross-type reversal cardinality

* Decision absent: whether a processed reference may have one successful
  reversal in total or separate successful reversals per applicable type, and
  how distinct reversal types interact.
* Why necessary: the prohibition on two successful reversals of the same
  applicable type does not determine the cardinality across distinct types.
* Sources inspected: AGENTS.md Financial Rules; ARCHITECTURE.md section 15;
  TASKS.md Loop 3; reversal migration and repository as auxiliary evidence
  only.
* Possible options, not selected: one successful reversal per reference, one
  per reversal type, or an explicitly constrained type combination.

### GAP-REFERENCE-002 — Referenced game validation

* Decision absent: whether the game identity of a reversal must match the game
  identity of the referenced transaction.
* Why necessary: the transaction may carry game identity, but the existing
  reference-validation decisions do not determine whether it participates in
  reference matching.
* Sources inspected: the original truncated SPEC.md; AGENTS.md; ARCHITECTURE.md
  section 15; TASKS.md Loop 3; current implementation as auxiliary evidence
  only.
* Possible options, not selected: require a matching game identity, ignore it
  for reference validation, or apply a separately defined relationship.

### GAP-IDEMPOTENCY-001 — Replay result and internal identity details

* Decision absent: the exact scope and fields of the applicable idempotency
  identity; the exact persisted replay-result schema; the states for which a
  replay result is returned; and any conflict behavior involving an internal
  transaction identity beyond the external identity and idempotency cases
  already specified.
* Why necessary: persistent idempotency requires a reusable result, but the
  sources do not define its complete representation or those additional
  identity rules.
* Sources inspected: AGENTS.md Idempotency; ARCHITECTURE.md sections 11–12;
  TASKS.md Loop 4; current idempotency implementation as auxiliary evidence
  only.
* Possible options, not selected: define a versioned result snapshot, persist
  the complete application result, or specify state-specific replay behavior.

### GAP-LEDGER-001 — Ledger entry schema and movement representation

* Decision absent: exact ledger-entry fields, whether direction and value are
  stored, whether balanceBefore and balanceAfter are stored, the formal
  movement equations, and the associated currency/amount constraints.
* Why necessary: append-only auditability and one-entry-per-effective-mutation
  do not by themselves determine the ledger record schema or movement model.
* Sources inspected: AGENTS.md Ledger and Transactions; ARCHITECTURE.md
  sections 8 and 13; TASKS.md Loops 2 and 3; migrations and implementation as
  auxiliary evidence only.
* Possible options, not selected: balance snapshots, signed entries, or a
  separate movement model with independently reconstructed balances.

### GAP-HTTP-001 — HTTP wire schemas

* Decision absent: exact request and response schemas, required/optional
  fields, list/ordering behavior where applicable, and the external
  representation of the persisted balance/result for each listed endpoint.
* Why necessary: clients and integration tests need an unambiguous wire
  contract, especially for money and replay results.
* Sources inspected: existing SPEC.md; ARCHITECTURE.md sections 20–25;
  AGENTS.md; TASKS.md Loop 6; Loop 0–5 application result types as auxiliary
  evidence.
* Possible options, not selected: an OpenAPI contract, endpoint-specific JSON
  schemas, or a separately versioned API contract.

### GAP-HTTP-002 — HTTP status codes and error envelope

* Decision absent: status-code mapping and error-body format for validation,
  business rejection, idempotency conflict, replay unavailable, not found,
  authentication failure, authorization failure and dependency failure.
* Why necessary: typed application errors do not by themselves define a
  transport contract.
* Sources inspected: AGENTS.md Context and Errors; ARCHITECTURE.md sections
  20–22; TASKS.md Loop 6 HTTP error contract; existing SPEC.md.
* Possible options, not selected: a problem-details envelope, a project error
  envelope, or endpoint-specific responses.

### GAP-AUTH-001 — Provider identity claim

* Decision absent: the exact OIDC claim or claim mapping that supplies
  providerId, including behavior when it is absent or ambiguous.
* Why necessary: provider isolation cannot be tested or enforced precisely
  without a deterministic identity mapping.
* Sources inspected: AGENTS.md Authentication and Authorization;
  ARCHITECTURE.md sections 21–22; TASKS.md Loops 6 and 10; local Keycloak
  realm configuration.
* Possible options, not selected: sub, a dedicated provider claim, or a
  namespaced claim mapping.

### GAP-AUTH-002 — OIDC roles, scopes and audience

* Decision absent: required roles, scopes, audience, issuer/JWKS validation
  details, clock-skew policy and the authorization matrix for each endpoint.
* Why necessary: authentication and authorization are separate guarantees, and
  a valid token alone does not establish permission.
* Sources inspected: AGENTS.md Authentication and Authorization;
  ARCHITECTURE.md sections 21–22 and 31; TASKS.md Loop 6; local Keycloak realm
  configuration.
* Possible options, not selected: role-based access, scope-based access, or a
  combination with audience validation.

### GAP-HTTP-003 — Wallet opening and health access policy

* Decision absent: who may invoke the listed POST /wallets endpoint, its
  internal/external transport boundary, the identity and authorization it
  requires, and whether health endpoints require authentication.
* Why necessary: the route is listed, and the sources distinguish internal
  wallet opening from provider operations, but do not define the access policy
  or health exposure.
* Sources inspected: AGENTS.md Authentication and Authorization;
  ARCHITECTURE.md sections 21–25; TASKS.md Loops 6 and 11; existing SPEC.md.
* Possible options, not selected: an internal caller identity, a separately
  authenticated administrative identity, or an explicitly prohibited provider
  identity.

### GAP-HEALTH-001 — Readiness contract

* Decision absent: exact readiness status/body, probe timeout and interval,
  dependency set beyond the stated primary PostgreSQL/SQS checks, and startup
  behavior while migrations or provisioning are incomplete.
* Why necessary: deployment orchestration needs a deterministic readiness
  contract.
* Sources inspected: ARCHITECTURE.md section 25; TASKS.md Loops 6 and 10;
  Docker Compose health checks; existing SPEC.md.
* Possible options, not selected: dependency probes only, a startup state
  machine, or a health response with per-dependency details.

### GAP-SQS-001 — SQS message contract

* Decision absent: exact message envelope, command field names and types,
  message-ID source, message attributes, FIFO message-group policy and
  validation rules.
* Why necessary: HTTP/SQS equivalence and durable inbox identity depend on a
  stable message contract.
* Sources inspected: AGENTS.md HTTP and SQS; ARCHITECTURE.md sections 17–20
  and 29; TASKS.md Loop 7; LocalStack queue bootstrap.
* Possible options, not selected: a direct command envelope, a versioned event
  envelope, or a provider-defined envelope adapter.

### GAP-SQS-002 — Inbox duplicate and completion semantics

* Decision absent: exact inbox schema and fields; behavior for the same
  (consumerName, messageId) with a different payload hash; meaning of an
  existing record with null completedAt; and the exact recovery/claim behavior
  for an interrupted consumer.
* Why necessary: uniqueness alone does not define safe handling of malformed or
  partially completed deliveries.
* Sources inspected: AGENTS.md Inbox; ARCHITECTURE.md section 17;
  TASKS.md Loop 7; inbox migration and repository; existing SPEC.md.
* Possible options, not selected: reject hash mismatch, treat the message ID as
  authoritative, or maintain explicit inbox processing states.

### GAP-REFERENCE-001 — Pending-reference policy

* Decision absent: worker ownership and claiming, exact polling schedule,
  exponential-backoff formula, retry/TTL values, expiration behavior and the
  event emitted when a reference expires.
* Why necessary: PENDING_REFERENCE must eventually have deterministic
  processing, rejection or retention behavior across restarts.
* Sources inspected: AGENTS.md transactions and recovery; ARCHITECTURE.md
  section 16; TASKS.md Loop 8; current environment defaults as auxiliary
  evidence.
* Possible options, not selected: bounded attempts, time-based TTL, or a
  durable pending-work scheduler.

### GAP-OUTBOX-001 — Outbox publication and recovery policy

* Decision absent: event destination and protocol, publication-success
  definition, retry/backoff values, claim lease duration, abandoned-claim
  recovery threshold and transition criteria for FAILED.
* Why necessary: durable creation and stable IDs do not define how a publisher
  safely completes or abandons a claim.
* Sources inspected: AGENTS.md Outbox; ARCHITECTURE.md sections 18–19 and
  29; TASKS.md Loop 9; outbox migration and repository.
* Possible options, not selected: SQS publication, another broker, or an
  application callback; lease-based or timestamp-based claim recovery.

### GAP-EVENT-001 — Event payload and metadata semantics

* Decision absent: exact data schema for each event, meaning of version,
  correlation/causation generation rules, ordering guarantees, timestamp
  precision, event-ID generation strategy and the conditions for emitting each
  event, including WalletBalanceChanged.
* Why necessary: consumers need a stable integration contract even though the
  event envelope fields and stable retry identity are already specified.
* Sources inspected: ARCHITECTURE.md section 19; AGENTS.md transactions and
  outbox; TASKS.md Loop 9; current event construction as auxiliary evidence.
* Possible options, not selected: one versioned schema per event type, a common
  snapshot schema, or consumer-specific payload versions.

### GAP-FAILURE-001 — Infrastructure failure classification

* Decision absent: criteria for transient versus permanent failure, when and
  how FAILED is durably recorded, retry ownership, deadlock/serialization
  retry behavior and the result exposed for an interrupted operation.
* Why necessary: the state machine names FAILED, while Loop 3 explicitly
  aborts infrastructure failures and defers registration/recovery to later
  loops.
* Sources inspected: AGENTS.md Transactions and Context/Errors;
  ARCHITECTURE.md sections 14 and 29; TASKS.md Loop 3 note and Loops 7, 9
  and 11; current financial service as auxiliary evidence.
* Possible options, not selected: retry then fail, an operator-reconciled
  failure state, or an inbox/outbox-owned failure record.

### GAP-RECON-001 — Reconciliation wire contract and remediation

* Decision absent: response schema, status/error behavior, authorization,
  whether reconciliation is read-only, and the controlled workflow for a
  detected divergence.
* Why necessary: detecting divergence and creating a correction are separate
  operations with different financial risks.
* Sources inspected: existing SPEC.md; ARCHITECTURE.md sections 13, 26 and
  29; TASKS.md Loops 3 and 6; current reconciliation use case as auxiliary
  evidence.
* Possible options, not selected: read-only reporting, an internal remediation
  command, or a separately approved correction workflow.

### GAP-MONEY-001 — Currency set and minor-unit policy

* Decision absent: the concrete validation strategy for ISO 4217 codes,
  whether every ISO 4217 code is supported or only a defined subset, and how
  currencies whose ISO minor units are not two decimal places are handled.
* Why necessary: the fixed two-decimal int64 representation is explicit for
  the current BRL scenario but does not by itself define the full currency
  domain.
* Sources inspected: existing SPEC.md sections 2–3; AGENTS.md Money;
  ARCHITECTURE.md section 6; current Money validation as auxiliary evidence.
* Possible options, not selected: BRL-only support, a configured two-decimal
  currency set, or currency metadata with per-currency minor units.

### GAP-LIFECYCLE-001 — In-flight shutdown policy

* Decision absent: graceful-shutdown deadline, whether in-flight HTTP/SQS work
  must finish or be released, visibility-timeout extension behavior and the
  exact worker stop order at deadline.
* Why necessary: finish or safely release is a required safety property but
  is not an executable lifecycle policy.
* Sources inspected: AGENTS.md Context; ARCHITECTURE.md section 24;
  TASKS.md Loops 7, 9 and 11; LOOPING.md sections 8, 11 and 14.
* Possible options, not selected: bounded drain, immediate cancellation with
  redelivery, or per-worker deadlines.
