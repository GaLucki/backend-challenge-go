-- Durable leases keep database transactions short; sequence orders aggregate heads.
CREATE SEQUENCE outbox_publication_sequence;
ALTER TABLE outbox_events
    ADD COLUMN publication_sequence BIGINT,
    ADD COLUMN claimed_at TIMESTAMPTZ,
    ADD COLUMN claim_until TIMESTAMPTZ,
    ADD COLUMN claimed_by TEXT,
    ADD COLUMN claim_token TEXT;

-- Existing envelopes/IDs remain untouched. Legacy rows get deterministic order.
WITH ordered AS (
    SELECT event_id, row_number() OVER (ORDER BY occurred_at, event_id) AS position
    FROM outbox_events
)
UPDATE outbox_events e SET publication_sequence=o.position FROM ordered o WHERE e.event_id=o.event_id;
SELECT setval('outbox_publication_sequence', COALESCE(MAX(publication_sequence),1), COUNT(*)>0)
FROM outbox_events;
ALTER SEQUENCE outbox_publication_sequence OWNED BY outbox_events.publication_sequence;
ALTER TABLE outbox_events
    ALTER COLUMN publication_sequence SET DEFAULT nextval('outbox_publication_sequence'),
    ALTER COLUMN publication_sequence SET NOT NULL,
    ADD CONSTRAINT outbox_publication_sequence_unique UNIQUE(publication_sequence),
    ADD CONSTRAINT outbox_claim_complete CHECK (
        (claimed_at IS NULL AND claim_until IS NULL AND claimed_by IS NULL AND claim_token IS NULL)
        OR (claimed_at IS NOT NULL AND claim_until IS NOT NULL AND claim_until>claimed_at
            AND claimed_by IS NOT NULL AND btrim(claimed_by)<>''
            AND claim_token IS NOT NULL AND btrim(claim_token)<>'' AND published_at IS NULL));
CREATE INDEX outbox_unpublished_aggregate_idx ON outbox_events(aggregate_id,publication_sequence)
    WHERE published_at IS NULL;
