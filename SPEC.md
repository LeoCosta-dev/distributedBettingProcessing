# Backend Challenge — Distributed Wagering Processing

## 0. Normative Scope and Restoration Provenance

This document is the normative functional and technical specification for the
distributed wagering transaction processor.

SPEC.md was originally truncated before the implementation loops were planned.
Its first restoration was performed without access to the complete primary
challenge source and therefore preserved some decisions as gaps. The complete
primary source was later recovered locally as CHALLENGE.md. This reconciliation
restores requirements that are explicit in that source and preserves only
decisions that the challenge genuinely leaves open.

The source precedence used for this reconciliation is:

1. CHALLENGE.md, the primary normative source;
2. this SPEC.md, as the internal derived specification;
3. ARCHITECTURE.md, for architectural decisions and interpretations;
4. TASKS.md, as the incremental execution plan;
5. implementation, migrations and tests, only as auxiliary evidence and never
   as a source for inventing requirements.

The sections before Open Specification Gaps contain explicit and derived
normative requirements restored from the primary source. A gap is not a
requirement: it records a decision that the challenge still leaves open.
Human decisions selected later for a loop must not be represented as if they
had been recovered from CHALLENGE.md.

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
* Empty values, NaN and Infinity are rejected.
* Scientific notation is rejected.
* Excessive decimal scale is rejected.
* Invalid values are rejected instead of silently rounded.
* Arithmetic between different currencies is rejected.
* Integer overflow must be detected if int64 is used.
* Persistence must preserve the exact amount and currency.
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

Wallet creation and rehydration are separate operations. A positive opening
creates an internal OPENING transaction in PROCESSED, one CREDIT ledger entry
and the corresponding WagerTransactionProcessed and WalletBalanceChanged
outbox events in the same commit. A zero opening creates the wallet without an
OPENING transaction, ledger entry or those financial events. A duplicate
(playerId, currency) opening is rejected as a conflict.

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

OPENING requires a stable internal identity, wallet, player, currency, value,
state and timestamps. External provider, external ID, idempotency key, payload
hash, round, game and reference fields do not apply to its internal origin.
The persistence model must distinguish internal and external operations and
prevent duplicate initial credit.

### 5.2 Identity and fields

A wagering transaction contains, when applicable:

* internal transaction ID;
* external transaction ID;
* providerId;
* walletId;
* playerId;
* gameId;
* roundId;
* transaction kind;
* exact money amount and currency;
* state;
* idempotency key;
* canonical payload hash;
* persisted processing result;
* referenceExternalTransactionId for reversal operations.

When applicable it also persists the resolved internal reference and a stable
failureCode. The persisted result is the exact result returned to the
provider, including the original balance snapshot.

The external transaction identity is (providerId, externalTransactionId)
and must be unique. The applicable idempotency identity is also unique in the
database. Transaction and wallet currencies must match.

The transaction player, wallet, provider and business identity must be
validated before a financial mutation is applied.

### 5.3 Operation rules

The normative operation matrix is:

| Type | Movement | Required behavior |
| --- | --- | --- |
| BET | DEBIT | Positive value and sufficient balance. Insufficient balance is a durable REJECTED result with no wallet or ledger mutation. |
| WIN | CREDIT | Positive value; may reference a BET from the same round. |
| LOSS | None | Amount exactly `0.00`; no ledger entry and no wallet-version change. A processed LOSS emits WagerTransactionProcessed but not WalletBalanceChanged. |
| REFUND | CREDIT | Positive value; returns the full value of a processed BET. The reference is mandatory. |
| ROLLBACK | Opposite of original | Positive value; fully reverses a processed BET, WIN or REFUND. The reference is mandatory. |

REFUND and ROLLBACK references resolve by `(providerId,
referenceExternalTransactionId)`. The operation and reference must agree in
provider, player, wallet, currency and round. The reversal amount must equal
the referenced amount; partial reversals are not supported. A reference cannot
receive two successful reversals of the same type. The interaction between
distinct reversal types remains open. A reversal that would debit more than
the available balance is rejected and audited with a failureCode distinct from
insufficient balance on BET.

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
(providerId, referenceExternalTransactionId)
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

A reference worker retries with exponential backoff, including after restart.
The policy must choose a maximum attempt count or TTL. Exhaustion produces a
REJECTED result with a stable reference-not-found failureCode and rejection
event. The behavior when the referenced transaction is still pending or has
ended unsuccessfully must be defined by the pending-reference policy.

The system must not allow two successful reversals of the same applicable
type. The interaction and cardinality between distinct reversal types are not
determined; see Open Specification Gaps.

---

## 6. Wallet Opening

Wallet opening is an internal application operation. It creates a wallet with
the requested player, currency and non-negative opening balance, subject to the
(playerId, currency) uniqueness invariant.

For a positive opening balance, the operation creates an internal OPENING
transaction in PROCESSED, a CREDIT ledger entry and the corresponding
WagerTransactionProcessed and WalletBalanceChanged events atomically with
wallet creation. The opening version is 1. A zero opening creates only wallet
state and no OPENING transaction, ledger entry or financial events. A duplicate
opening for the same `(playerId, currency)` is a conflict.

The internal opening operation must not be exposed as a provider wagering
operation. External callers cannot submit OPENING through the wagering
transaction flow.

---

## 7. Ledger

The ledger is the append-only authoritative audit trail for reconciliation.

Each ledger entry contains id, walletId, transactionId, direction, value,
balanceBefore, balanceAfter and creation timestamp. Direction is DEBIT or
CREDIT, and the value and balances carry the wallet currency. Ledger entries
are immutable and cannot be updated or deleted. Corrections create new
financial entries instead of modifying previous entries. The relationship
(walletId, transactionId) must prevent more than one corresponding ledger
entry for the same wallet transaction.

Ledger construction validates the movement equation:

~~~text
CREDIT: balanceAfter = balanceBefore + value
DEBIT:  balanceAfter = balanceBefore - value
~~~

The database imposes uniqueness and protection against editing or deleting
entries. LOSS and rejected operations do not create ledger entries.

Every effective balance mutation creates exactly one corresponding ledger entry
committed atomically with the balance update and transaction state.


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

The canonical payload must use deterministic JSON key ordering. The
idempotency key, caller-provided hash and transport metadata are excluded.
The algorithm, complete business-field set, exact ordering and normalization
rules are not selected by this specification; they must be documented by the
chosen architecture and shared by HTTP and SQS before the application command
is executed. The service calculates the hash; a caller-provided hash is not
authoritative.

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

The handling of an existing record without a compatible canonical hash or a
valid persisted result is not selected by this specification; see
GAP-IDEMPOTENCY-001. No behavior for that edge case is restored from the
primary source here.

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

### 10.1 Primary wire contracts

The primary wallet-opening request is:

~~~json
{
  "playerId": "0192f28f-5dc0-7d58-bdb2-814ad6a0f4a1",
  "initialBalance": { "amount": "1000.00", "currency": "BRL" }
}
~~~

Its documented response contains `id`, `playerId`, `balance` and `version`.
A positive opening creates OPENING and its ledger/events in the same commit;
zero opening creates no financial movement; a duplicate `(playerId, currency)`
opening is a conflict.

The primary wagering request uses these names:

~~~json
{
  "providerId": "provider-a",
  "externalTransactionId": "transaction-123",
  "playerId": "0192f28f-5dc0-7d58-bdb2-814ad6a0f4a1",
  "walletId": "0192f291-27dd-7d3f-8071-5f8685deef37",
  "roundId": "round-987",
  "gameId": "fortune-chimp",
  "kind": "BET",
  "money": { "amount": "25.00", "currency": "BRL" }
}
~~~

The `Idempotency-Key` header is mandatory. The documented processed response
uses `transactionId`, `status`, `balance` and `idempotentReplay`. For reversal
requests, `referenceExternalTransactionId` is added to the body. The
authenticated identity is authoritative for provider authorization; a
providerId supplied by a caller must never override it.

The primary reconciliation response contains `walletId`, `storedBalance`,
`calculatedBalance`, `difference`, `consistent` and `checkedEntries`.
`difference` is stored balance minus reconstructed ledger balance. Reconciliation
is read-only and divergences are reported in the response, structured logs and
a metric.

Business endpoints require real OAuth 2.0/OIDC authentication and provider
authorization. Provider-scoped transaction data, including replays, must be
isolated by provider before any data access or financial side effect.

This section records the schemas and names explicitly provided by the primary
source. It intentionally does not assign HTTP status codes, an error envelope,
or an exact token claim; those remain open gaps below. The primary field names
must not be replaced by implementation-specific aliases.

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
provisions the FIFO queues `wager-transactions.fifo` and
`wager-transactions-dlq.fifo` with redrive configuration. FIFO deduplication is
not the financial idempotency mechanism.

The requested message envelope contains `messageId`, `type`, `occurredAt` and
`data`. The wagering `data` contains providerId, externalTransactionId,
idempotencyKey, playerId, walletId, roundId, gameId, kind and the `{amount,
currency}` money object. `data.idempotencyKey` is the financial idempotency
key. The consumer uses the envelope messageId as its durable message identity
and validates the message hash on redelivery.

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

Business rejections are terminal and may be deleted after durable commit.
Transient failures use retry with backoff. Permanent failures or exhausted
attempts reach the DLQ. The implementation must document malformed-message
handling, attempt limits, visibility timeout, MessageGroupId and
MessageDeduplicationId. On SIGTERM, polling stops and in-flight work either
finishes within the deadline or releases visibility for safe redelivery.

---

## 13. Inbox

Inbox records are durable PostgreSQL records. They include message identity,
consumer identity, payload hash, receipt and completion information. Their
identity is:

~~~text
(consumerName, messageId)
~~~

The database enforces uniqueness for that identity. The exact interrupted
processing state, hash-mismatch behavior and claim/recovery representation
remain open; see Open Specification Gaps.

Inbox completion and the financial operation caused by the message share the
same database transaction. A duplicate message must not cause the financial
operation to execute again, and an uncommitted transaction must not leave a
completed inbox record behind.

---

## 14. Transactional Outbox

Integration events are created in the same PostgreSQL transaction as the state
changes they describe. Outbox publication is asynchronous and starts only
after commit.

The outbox stores a stable event identity, aggregate, event type, immutable
payload snapshot, occurrence time, attempts, next-send time and publication
state. The publisher must survive the windows between commit and publication
and between publication and confirmation, including by allowing another
publisher to recover abandoned work.

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

The event triggers are:

| Event | Trigger |
| --- | --- |
| WagerTransactionProcessed | Successful completion of an operation, including LOSS. |
| WagerTransactionRejected | Definitive business rejection. |
| WalletBalanceChanged | Effective wallet balance change. |
| WagerTransactionPendingReference | Durable wait for a missing reference. |

Events contain:

~~~text
eventId
eventType
aggregateId
correlationId
causationId (optional)
occurredAt
version
data
~~~

Event payloads are immutable snapshots of the state they describe. Money is
serialized as decimal strings, and timestamps use UTC RFC 3339. The exact
conditions above are normative. The WalletBalanceChanged payload contains
walletId, transactionId, direction, money, balanceBefore, balanceAfter and
walletVersion. Event type and version are assigned by the event constructor.
The exact version values, correlation/causation generation, ordering and
additional event-specific data remain open; see Open Specification Gaps.

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

Its response contains `walletId`, `storedBalance`, `calculatedBalance`,
`difference`, `consistent` and `checkedEntries`. `difference` is the stored
balance minus the reconstructed ledger balance. Reconciliation is read-only,
and divergences are reported in the response, structured logs and a metric.
HTTP status mapping, concrete authorization and remediation policy remain
open.

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

Liveness indicates that the process is alive. Readiness checks PostgreSQL and
SQS. A dependency outage makes
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

The required scenarios include fifty concurrent duplicate requests for one
operation with exactly one financial movement; the two concurrent 80.00 BETs
against a 100.00 wallet; independent wallets progressing in parallel; at least
three independent application processes; consumer interruption after commit
and before message deletion; competing outbox publishers; late REFUND or
ROLLBACK references; application restart; and the same operation crossing
HTTP and SQS. Integration tests use real PostgreSQL, the IdP and LocalStack or
MiniStack containers where applicable.

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

* Decision absent: the interaction and cardinality between distinct reversal
  types, including whether one referenced transaction may receive more than
  one successful reversal of different types and how those combinations are
  constrained.
* Why necessary: CHALLENGE.md defines the operation matrix, same-type
  duplicate protection and the need to document cross-type combinations, but
  does not select the cross-type policy.
* Sources inspected: CHALLENGE.md sections 7–8; the restored SPEC.md;
  AGENTS.md Financial Rules; ARCHITECTURE.md section 15; TASKS.md Loop 3.
* Possible options, not selected: one successful reversal per reference, one
  per reversal type, or an explicitly constrained type combination.

### GAP-REVERSAL-001 — Cross-type reversal cardinality

* Decision absent: whether a processed reference may have one successful
  reversal in total or separate successful reversals per applicable type, and
  how distinct reversal types interact.
* Why necessary: the prohibition on two successful reversals of the same
  applicable type does not determine the cardinality across distinct types.
* Sources inspected: CHALLENGE.md section 7; AGENTS.md Financial Rules;
  ARCHITECTURE.md section 15; TASKS.md Loop 3; reversal migration and
  repository as auxiliary evidence only.
* Possible options, not selected: one successful reversal per reference, one
  per reversal type, or an explicitly constrained type combination.

### GAP-REFERENCE-002 — Referenced game validation

* Decision absent: whether the game identity of a reversal must match the game
  identity of the referenced transaction.
* Why necessary: the transaction may carry game identity, but the existing
  reference-validation decisions do not determine whether it participates in
  reference matching.
* Sources inspected: CHALLENGE.md section 7; AGENTS.md; ARCHITECTURE.md
  section 15; TASKS.md Loop 3; current implementation as auxiliary evidence
  only.
* Possible options, not selected: require a matching game identity, ignore it
  for reference validation, or apply a separately defined relationship.

### GAP-IDEMPOTENCY-001 — Replay result and internal identity details

* Decision absent from CHALLENGE.md: details beyond the explicit idempotency
  contract, including the exact database scope of each identity, the physical
  representation of the persisted result snapshot, and edge behavior for
  states or conflicts not covered by the primary contract. The primary source
  requires the hash algorithm, business fields and normalizations to be
  documented but does not select them; any later architectural choice for
  those details is not a restored challenge requirement.
* Why necessary: CHALLENGE.md fixes the Idempotency-Key, deterministic
  canonical JSON, excluded inputs, same-key outcomes, external-identity
  behavior and original balance snapshot, but does not define every storage,
  hashing or edge-case detail.
* Sources inspected: CHALLENGE.md sections 9–10; AGENTS.md Idempotency;
  ARCHITECTURE.md sections 11–12; TASKS.md Loop 4; current implementation as
  auxiliary evidence only.
* Possible options, not selected by this SPEC: a versioned result snapshot, a
  complete application-result record, state-specific replay rules, or a later
  architectural choice for the hashing details delegated by the challenge.

### GAP-HTTP-001 — HTTP wire schemas

* Decision absent: complete schemas for endpoints and fields not fully defined
  by the primary examples, including optionality, list ordering and remaining
  response details.
* Why necessary: CHALLENGE.md defines the required wire names and principal
  examples, but does not provide a complete schema for every listed endpoint.
* Sources inspected: CHALLENGE.md section 9; existing SPEC.md;
  ARCHITECTURE.md sections 20–25; AGENTS.md; TASKS.md Loop 6; Loop 0–5
  application result types as auxiliary evidence.
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
* Sources inspected: CHALLENGE.md sections 2 and 9; AGENTS.md Authentication
  and Authorization; ARCHITECTURE.md sections 21–22; TASKS.md Loops 6 and 10;
  local Keycloak realm configuration.
* Possible options, not selected: sub, a dedicated provider claim, or a
  namespaced claim mapping.

### GAP-AUTH-002 — OIDC roles, scopes and audience

* Decision absent: required roles, scopes, audience, issuer/JWKS validation
  details, clock-skew policy and the authorization matrix for each endpoint.
* Why necessary: authentication and authorization are separate guarantees, and
  a valid token alone does not establish permission.
* Sources inspected: CHALLENGE.md section 2; AGENTS.md Authentication and
  Authorization; ARCHITECTURE.md sections 21–22 and 31; TASKS.md Loop 6; local
  Keycloak realm configuration.
* Possible options, not selected: role-based access, scope-based access, or a
  combination with audience validation.

### GAP-HTTP-003 — Wallet opening and health access policy

* Decision absent: the concrete internal identity and transport/authorization
  mechanism used for wallet operations, and the network binding/exposure
  details of the public health routes.
* Why necessary: CHALLENGE.md requires wallet operations to be internal and
  health to be public, but does not select the concrete identity, boundary or
  authorization mechanism.
* Sources inspected: CHALLENGE.md sections 2 and 9; AGENTS.md
  Authentication and Authorization; ARCHITECTURE.md sections 21–25; TASKS.md
  Loops 6 and 11; existing SPEC.md.
* Possible options, not selected: an internal caller identity, an
  administrator identity, or an explicitly defined transport boundary.

### GAP-HEALTH-001 — Readiness contract

* Decision absent: exact readiness status/body, probe timeout and interval,
  startup behavior while migrations or provisioning are incomplete, and probe
  details beyond PostgreSQL and SQS.
* Why necessary: CHALLENGE.md fixes PostgreSQL and SQS as readiness
  dependencies, but deployment orchestration still needs a deterministic
  response and startup policy.
* Sources inspected: CHALLENGE.md sections 9 and 12; ARCHITECTURE.md section 25;
  TASKS.md Loops 6 and 10; Docker Compose health checks; existing SPEC.md.
* Possible options, not selected: dependency probes only, a startup state
  machine, or a health response with per-dependency details.

### GAP-SQS-001 — SQS message contract

* Decision absent: optional envelope/attribute fields, exact validation rules,
  and the concrete FIFO MessageGroupId and MessageDeduplicationId policy.
* Why necessary: CHALLENGE.md fixes the queue names, FIFO/DLQ arrangement,
  envelope and principal command fields, but leaves these implementation
  details open.
* Sources inspected: CHALLENGE.md section 10; AGENTS.md HTTP and SQS;
  ARCHITECTURE.md sections 17–20 and 29; TASKS.md Loop 7; LocalStack queue
  bootstrap.
* Possible options, not selected: a direct command envelope, a versioned event
  envelope, or a provider-defined envelope adapter.

### GAP-SQS-002 — Inbox duplicate and completion semantics

* Decision absent: behavior for a hash mismatch, an incomplete/null completion
  record, and the exact claim/recovery state machine for an interrupted
  consumer.
* Why necessary: CHALLENGE.md fixes durable identity, payload hash,
  transaction-bound completion and duplicate suppression, but does not choose
  the handling policy for these exceptional records.
* Sources inspected: CHALLENGE.md sections 6.5 and 10; AGENTS.md Inbox;
  ARCHITECTURE.md section 17; TASKS.md Loop 7; inbox migration and repository;
  existing SPEC.md.
* Possible options, not selected: reject hash mismatch, treat message ID as
  authoritative, or maintain explicit inbox processing states.

### GAP-REFERENCE-001 — Pending-reference policy

* Decision absent: worker ownership and claiming, exact polling schedule and
  backoff formula/values, retry/TTL values, and details of handling a pending
  or unsuccessful referenced transaction.
* Why necessary: CHALLENGE.md fixes restartable exponential backoff and
  exhaustion as REJECTED with a stable reference-not-found code and rejection
  event, but does not choose the concrete schedule or ownership policy.
* Sources inspected: CHALLENGE.md sections 7 and 13; AGENTS.md transactions
  and recovery; ARCHITECTURE.md section 16; TASKS.md Loop 8; current
  environment defaults as auxiliary evidence.
* Possible options, not selected: bounded attempts, time-based TTL, or a
  durable pending-work scheduler.

### GAP-OUTBOX-001 — Outbox publication and recovery policy

* Decision absent: destination/protocol, publication-success definition,
  exact retry/backoff values, claim lease duration, abandonment threshold and
  transition criteria for a terminal failure.
* Why necessary: CHALLENGE.md fixes atomic creation, stable identity,
  multiple publishers, claiming, retries, backoff, abandoned-work recovery
  and the relevant publication windows, but does not choose these concrete
  policies.
* Sources inspected: CHALLENGE.md section 11; AGENTS.md Outbox;
  ARCHITECTURE.md sections 18–19 and 29; TASKS.md Loop 9; outbox migration
  and repository.
* Possible options, not selected: SQS publication, another broker, or an
  application callback; lease-based or timestamp-based claim recovery.

### GAP-EVENT-001 — Event payload and metadata semantics

* Decision absent: version values, correlation/causation generation rules,
  ordering guarantees, event-ID generation strategy and additional data
  schemas beyond the explicitly defined event payloads.
* Why necessary: CHALLENGE.md fixes the four event types, their principal
  triggers, envelope, immutable snapshot, UTC timestamps and monetary string
  representation, but does not define every metadata policy.
* Sources inspected: CHALLENGE.md sections 11 and 12; ARCHITECTURE.md section 19;
  AGENTS.md transactions and outbox; TASKS.md Loop 9; current event
  construction as auxiliary evidence.
* Possible options, not selected: one versioned schema per event type, a common
  snapshot schema, or consumer-specific payload versions.

### GAP-FAILURE-001 — Infrastructure failure classification

* Decision absent: exact criteria for transient versus permanent failure,
  retry ownership, deadlock/serialization retry policy, durable FAILED
  recording details and the result exposed for an interrupted operation.
* Why necessary: CHALLENGE.md requires no partial financial mutation and
  distinguishes transient retry/backoff from permanent or exhausted DLQ
  handling, but does not define every classification or ownership rule.
* Sources inspected: CHALLENGE.md sections 3 and 10; AGENTS.md Transactions and
  Context/Errors; ARCHITECTURE.md sections 14 and 29; TASKS.md Loop 3 note and
  Loops 7, 9 and 11; current financial service as auxiliary evidence.
* Possible options, not selected: retry then fail, an operator-reconciled
  failure state, or an inbox/outbox-owned failure record.

### GAP-RECON-001 — Reconciliation wire contract and remediation

* Decision absent: HTTP status/error mapping, concrete authorization
  mechanism and any remediation workflow for a detected divergence.
* Why necessary: CHALLENGE.md fixes the reconciliation response fields,
  stored-minus-calculated difference, read-only behavior and divergence
  response/log/metric, but does not authorize a correction workflow or define
  the remaining transport details.
* Sources inspected: CHALLENGE.md section 12; existing SPEC.md;
  ARCHITECTURE.md sections 13, 26 and 29; TASKS.md Loops 3 and 6; current
  reconciliation use case as auxiliary evidence.
* Possible options, not selected: read-only reporting, an internal remediation
  command, or a separately approved correction workflow.

### GAP-MONEY-001 — Currency set and minor-unit policy

* Decision absent: the concrete validation strategy for ISO 4217 codes,
  whether every ISO 4217 code is supported or only a defined subset, and how
  currencies whose ISO minor units are not two decimal places are handled.
* Why necessary: the fixed two-decimal int64 representation is explicit for
  the current BRL scenario but does not by itself define the full currency
  domain.
* Sources inspected: CHALLENGE.md section 6.1; existing SPEC.md sections 2–3;
  AGENTS.md Money; ARCHITECTURE.md section 6; current Money validation as
  auxiliary evidence.
* Possible options, not selected: BRL-only support, a configured two-decimal
  currency set, or currency metadata with per-currency minor units.

### GAP-LIFECYCLE-001 — In-flight shutdown policy

* Decision absent: exact graceful-shutdown deadline, visibility-timeout
  extension behavior and worker stop order at the deadline.
* Why necessary: CHALLENGE.md requires stopping acceptance/polling and
  finishing or safely releasing in-flight work within a deadline, but does not
  choose the concrete deadline or worker policy.
* Sources inspected: CHALLENGE.md sections 4 and 13; AGENTS.md Context;
  ARCHITECTURE.md section 24; TASKS.md Loops 7, 9 and 11; LOOPING.md sections
  8, 11 and 14.
* Possible options, not selected: bounded drain, immediate cancellation with
  redelivery, or per-worker deadlines.
