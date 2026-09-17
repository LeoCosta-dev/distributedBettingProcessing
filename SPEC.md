# Backend Challenge — Distributed Wagering Processing

## 1. Purpose

Implement a Go backend service capable of processing financial wagering operations from HTTP and SQS in a distributed environment.

The system must preserve financial correctness when multiple application instances process operations concurrently and when failures occur between processing stages.

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

# 2. Non-Negotiable Invariants

These invariants must always hold.

## 2.1 Money

* Monetary values must never use `float32` or `float64`.
* External monetary values use strings with exactly two decimal places.
* Currency uses ISO 4217 codes.
* External financial inputs cannot be negative.
* Scientific notation is rejected.
* Excessive decimal scale is rejected.
* Invalid values are rejected instead of silently rounded.
* Arithmetic between different currencies is rejected.
* Integer overflow must be detected if `int64` is used.
* Internal calculations may temporarily contain negative values.
* Wallet balances may never become negative.

## 2.2 Wallet

A wallet is uniquely identified by:

```text
(playerId, currency)
```

A wallet contains:

* `id`;
* `playerId`;
* `currency`;
* `balance`;
* `version`;
* `createdAt`;
* `updatedAt`.

The initial version is `1`.

The version increases only when the wallet balance changes.

Every effective financial balance mutation must have a corresponding ledger entry committed atomically with the balance update.

Concurrent updates must not lose confirmed changes.

Independent wallets must be able to progress concurrently.

---

# 3. Money Contract

External representation:

```json
{
  "amount": "25.00",
  "currency": "BRL"
}
```

`Money` must support:

* construction from decimal string;
* zero by currency;
* addition;
* subtraction;
* negation;
* comparison;
* serialization.

The main scenarios may operate only with BRL, provided the domain still carries currency and incompatible-currency behavior is tested.

---

# 4. Wager Transactions

Supported types:

```text
OPENING
BET
WIN
LOSS
REFUND
ROLLBACK
```

External operations:

```text
BET
WIN
LOSS
REFUND
ROLLBACK
```

`OPENING` is reserved for internal wallet creation.

External requests containing `OPENING` must be rejected.

A wagering transaction contains, when applicable:

* internal transaction ID;
* external transaction ID;
* provide
