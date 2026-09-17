ALTER TABLE wager_transactions
    ADD COLUMN player_id TEXT,
    ADD COLUMN round_id TEXT;

UPDATE wager_transactions
SET player_id = 'legacy', round_id = 'legacy'
WHERE player_id IS NULL OR round_id IS NULL;

ALTER TABLE wager_transactions
    ALTER COLUMN player_id SET NOT NULL,
    ALTER COLUMN round_id SET NOT NULL,
    ADD CONSTRAINT wager_transactions_player_nonempty CHECK (player_id <> ''),
    ADD CONSTRAINT wager_transactions_round_nonempty CHECK (round_id <> '');

ALTER TABLE wallets ADD CONSTRAINT wallets_id_currency_key UNIQUE (id, currency);
ALTER TABLE wager_transactions ADD CONSTRAINT wager_transactions_id_currency_key UNIQUE (id, currency);
ALTER TABLE wager_transactions ADD CONSTRAINT wager_transactions_wallet_currency_fk
    FOREIGN KEY (wallet_id, currency) REFERENCES wallets (id, currency);
ALTER TABLE wallet_ledger_entries ADD CONSTRAINT wallet_ledger_wallet_currency_fk
    FOREIGN KEY (wallet_id, currency) REFERENCES wallets (id, currency);
ALTER TABLE wallet_ledger_entries ADD CONSTRAINT wallet_ledger_transaction_currency_fk
    FOREIGN KEY (transaction_id, currency) REFERENCES wager_transactions (id, currency);

DROP INDEX IF EXISTS wager_transactions_reversal_idx;
CREATE UNIQUE INDEX wager_transactions_reversal_idx
    ON wager_transactions(provider_id, reference_external_id)
    WHERE reference_external_id IS NOT NULL AND state = 'PROCESSED';
