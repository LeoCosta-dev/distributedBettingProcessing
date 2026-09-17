ALTER TABLE wager_transactions DROP CONSTRAINT IF EXISTS wager_transactions_type_check;
ALTER TABLE wager_transactions ADD CONSTRAINT wager_transactions_type_check CHECK (type IN ('OPENING', 'BET', 'WIN', 'LOSS', 'REFUND', 'ROLLBACK'));
