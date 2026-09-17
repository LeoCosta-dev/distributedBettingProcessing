-- Rollback is intentionally refused while OPENING rows exist. Removing them
-- would destroy the financial origin of a wallet; archive them explicitly
-- before running this migration in a controlled maintenance procedure.
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM wager_transactions WHERE type = 'OPENING') THEN
        RAISE EXCEPTION 'cannot remove OPENING support while OPENING transactions exist';
    END IF;
END;
$$;

ALTER TABLE wager_transactions DROP CONSTRAINT IF EXISTS wager_transactions_type_check;
ALTER TABLE wager_transactions ADD CONSTRAINT wager_transactions_type_check CHECK (type IN ('BET', 'WIN', 'LOSS', 'REFUND', 'ROLLBACK'));
