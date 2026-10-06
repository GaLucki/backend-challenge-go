CREATE TABLE wager_idempotency_records (
    idempotency_key TEXT PRIMARY KEY CHECK (btrim(idempotency_key) <> ''),
    payload_hash TEXT NOT NULL CHECK (payload_hash ~ '^[0-9a-f]{64}$'),
    transaction_id TEXT UNIQUE REFERENCES wager_transactions(transaction_id),
    result JSONB,
    created_at TIMESTAMPTZ NOT NULL,
    completed_at TIMESTAMPTZ,
    CHECK ((result IS NULL AND transaction_id IS NULL AND completed_at IS NULL)
        OR (result IS NOT NULL AND transaction_id IS NOT NULL AND completed_at IS NOT NULL))
);
-- Claims and completion are made in the financial transaction; no incomplete claim
-- is committed by the application. Concurrent INSERT ON CONFLICT waits on that key.
