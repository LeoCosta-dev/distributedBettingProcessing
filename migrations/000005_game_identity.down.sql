ALTER TABLE wager_transactions
    DROP CONSTRAINT IF EXISTS wager_transactions_game_nonempty,
    DROP COLUMN IF EXISTS game_id;
