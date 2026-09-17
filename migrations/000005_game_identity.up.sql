ALTER TABLE wager_transactions ADD COLUMN game_id TEXT;

UPDATE wager_transactions
SET game_id = 'legacy'
WHERE game_id IS NULL;

ALTER TABLE wager_transactions
    ALTER COLUMN game_id SET NOT NULL,
    ADD CONSTRAINT wager_transactions_game_nonempty CHECK (game_id <> '');
