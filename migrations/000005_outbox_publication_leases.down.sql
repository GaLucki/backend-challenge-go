DROP INDEX outbox_unpublished_aggregate_idx;
ALTER TABLE outbox_events
    DROP CONSTRAINT outbox_claim_complete,
    DROP CONSTRAINT outbox_publication_sequence_unique,
    DROP COLUMN claimed_at,
    DROP COLUMN claim_until,
    DROP COLUMN claimed_by,
    DROP COLUMN claim_token,
    DROP COLUMN publication_sequence;
-- The owned sequence is dropped with its column. Financial envelopes are retained.
