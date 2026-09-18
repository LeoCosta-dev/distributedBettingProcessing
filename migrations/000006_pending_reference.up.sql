ALTER TABLE wager_transactions
    ADD COLUMN reference_attempts INTEGER NOT NULL DEFAULT 0 CHECK (reference_attempts >= 0),
    ADD COLUMN reference_next_attempt_at TIMESTAMPTZ,
    ADD COLUMN failure_code TEXT;

CREATE INDEX wager_transactions_pending_reference_idx
    ON wager_transactions (reference_next_attempt_at, id)
    WHERE state = 'PENDING_REFERENCE';
