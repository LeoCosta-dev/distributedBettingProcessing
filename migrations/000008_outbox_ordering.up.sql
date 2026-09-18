CREATE SEQUENCE IF NOT EXISTS outbox_ordering_seq;

ALTER TABLE outbox ADD COLUMN IF NOT EXISTS ordering_id BIGINT;

WITH base AS (
    SELECT COALESCE(MAX(ordering_id), 0) AS offset
    FROM outbox
), ordered_rows AS (
    SELECT event_id,
           base.offset + row_number() OVER (ORDER BY occurred_at, event_id) AS position
    FROM outbox
    CROSS JOIN base
    WHERE ordering_id IS NULL
)
UPDATE outbox AS target
SET ordering_id = ordered_rows.position
FROM ordered_rows
WHERE target.event_id = ordered_rows.event_id;

SELECT setval(
    'outbox_ordering_seq',
    GREATEST(COALESCE((SELECT MAX(ordering_id) FROM outbox), 0), 1),
    true
);

ALTER TABLE outbox
    ALTER COLUMN ordering_id SET DEFAULT nextval('outbox_ordering_seq'),
    ALTER COLUMN ordering_id SET NOT NULL;

ALTER SEQUENCE outbox_ordering_seq OWNED BY outbox.ordering_id;

CREATE INDEX IF NOT EXISTS outbox_aggregate_ordering_idx
    ON outbox (aggregate_id, ordering_id, status);
