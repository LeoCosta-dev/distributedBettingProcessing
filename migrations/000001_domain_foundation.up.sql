CREATE TABLE wallets (
    id UUID PRIMARY KEY,
    player_id TEXT NOT NULL CHECK (player_id <> ''),
    currency CHAR(3) NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
    balance BIGINT NOT NULL CHECK (balance >= 0),
    version BIGINT NOT NULL CHECK (version >= 1),
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL,
    UNIQUE (player_id, currency)
);

CREATE TABLE wager_transactions (
    id UUID PRIMARY KEY,
    external_id TEXT NOT NULL CHECK (external_id <> ''),
    provider_id TEXT NOT NULL CHECK (provider_id <> ''),
    wallet_id UUID NOT NULL REFERENCES wallets(id),
    type TEXT NOT NULL CHECK (type IN ('BET', 'WIN', 'LOSS', 'REFUND', 'ROLLBACK')),
    amount BIGINT NOT NULL CHECK (amount >= 0),
    currency CHAR(3) NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
    state TEXT NOT NULL CHECK (state IN ('PENDING', 'PROCESSED', 'REJECTED', 'FAILED', 'PENDING_REFERENCE')),
    idempotency_key TEXT NOT NULL CHECK (idempotency_key <> ''),
    payload_hash TEXT NOT NULL CHECK (payload_hash <> ''),
    result JSONB,
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL,
    UNIQUE (provider_id, external_id),
    UNIQUE (provider_id, idempotency_key),
    CHECK ((type = 'LOSS' AND amount = 0) OR (type <> 'LOSS' AND amount > 0))
);

CREATE TABLE wallet_ledger_entries (
    id UUID PRIMARY KEY,
    wallet_id UUID NOT NULL REFERENCES wallets(id),
    transaction_id UUID NOT NULL REFERENCES wager_transactions(id),
    direction TEXT NOT NULL CHECK (direction IN ('CREDIT', 'DEBIT')),
    value BIGINT NOT NULL CHECK (value > 0),
    currency CHAR(3) NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
    balance_before BIGINT NOT NULL CHECK (balance_before >= 0),
    balance_after BIGINT NOT NULL CHECK (balance_after >= 0),
    timestamp TIMESTAMPTZ NOT NULL,
    UNIQUE (wallet_id, transaction_id),
    CHECK ((direction = 'CREDIT' AND balance_after = balance_before + value) OR
           (direction = 'DEBIT' AND balance_after = balance_before - value))
);

CREATE TABLE inbox (
    consumer_name TEXT NOT NULL CHECK (consumer_name <> ''),
    message_id TEXT NOT NULL CHECK (message_id <> ''),
    payload_hash TEXT NOT NULL CHECK (payload_hash <> ''),
    received_at TIMESTAMPTZ NOT NULL,
    completed_at TIMESTAMPTZ,
    PRIMARY KEY (consumer_name, message_id)
);

CREATE TABLE outbox (
    event_id UUID PRIMARY KEY,
    event_type TEXT NOT NULL,
    aggregate_id UUID NOT NULL,
    correlation_id TEXT,
    causation_id TEXT,
    occurred_at TIMESTAMPTZ NOT NULL,
    version BIGINT NOT NULL CHECK (version >= 1),
    data JSONB NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('PENDING', 'CLAIMED', 'PUBLISHED', 'FAILED')),
    attempts INTEGER NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    next_attempt_at TIMESTAMPTZ NOT NULL,
    claimed_at TIMESTAMPTZ,
    published_at TIMESTAMPTZ,
    last_error TEXT
);

CREATE FUNCTION reject_ledger_mutation() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'wallet ledger entries are immutable';
END;
$$;

CREATE TRIGGER wallet_ledger_entries_immutable
    BEFORE UPDATE OR DELETE ON wallet_ledger_entries
    FOR EACH ROW EXECUTE FUNCTION reject_ledger_mutation();

CREATE INDEX wager_transactions_wallet_idx ON wager_transactions(wallet_id);
CREATE INDEX outbox_next_attempt_idx ON outbox(status, next_attempt_at);
