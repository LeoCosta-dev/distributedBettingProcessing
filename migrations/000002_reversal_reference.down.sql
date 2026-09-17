DROP INDEX IF EXISTS wager_transactions_reversal_idx;
ALTER TABLE wager_transactions DROP CONSTRAINT IF EXISTS wager_transactions_reference_nonempty;
ALTER TABLE wager_transactions DROP COLUMN IF EXISTS reference_external_id;
