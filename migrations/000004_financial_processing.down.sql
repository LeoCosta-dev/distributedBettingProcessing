DROP INDEX IF EXISTS wager_transactions_reversal_idx;
CREATE UNIQUE INDEX wager_transactions_reversal_idx
    ON wager_transactions(provider_id, reference_external_id, type)
    WHERE reference_external_id IS NOT NULL AND state = 'PROCESSED';

ALTER TABLE wallet_ledger_entries DROP CONSTRAINT IF EXISTS wallet_ledger_transaction_currency_fk;
ALTER TABLE wallet_ledger_entries DROP CONSTRAINT IF EXISTS wallet_ledger_wallet_currency_fk;
ALTER TABLE wager_transactions DROP CONSTRAINT IF EXISTS wager_transactions_wallet_currency_fk;
ALTER TABLE wager_transactions DROP CONSTRAINT IF EXISTS wager_transactions_id_currency_key;
ALTER TABLE wallets DROP CONSTRAINT IF EXISTS wallets_id_currency_key;

ALTER TABLE wager_transactions
    DROP CONSTRAINT IF EXISTS wager_transactions_round_nonempty,
    DROP CONSTRAINT IF EXISTS wager_transactions_player_nonempty;

ALTER TABLE wager_transactions
    DROP COLUMN IF EXISTS round_id,
    DROP COLUMN IF EXISTS player_id;
