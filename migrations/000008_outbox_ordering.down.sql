DROP INDEX IF EXISTS outbox_aggregate_ordering_idx;
ALTER TABLE outbox DROP COLUMN IF EXISTS ordering_id;
DROP SEQUENCE IF EXISTS outbox_ordering_seq;
