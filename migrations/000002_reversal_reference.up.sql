ALTER TABLE wager_transactions ADD COLUMN reference_external_id TEXT;
ALTER TABLE wager_transactions ADD CONSTRAINT wager_transactions_reference_nonempty CHECK (reference_external_id IS NULL OR reference_external_id <> '');
CREATE UNIQUE INDEX wager_transactions_reversal_idx ON wager_transactions(provider_id, reference_external_id) WHERE reference_external_id IS NOT NULL AND state = 'PROCESSED';
