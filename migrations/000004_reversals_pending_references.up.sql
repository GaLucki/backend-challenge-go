CREATE TABLE wager_reversals (
    original_transaction_id TEXT PRIMARY KEY REFERENCES wager_transactions(transaction_id),
    reversal_transaction_id TEXT NOT NULL UNIQUE REFERENCES wager_transactions(transaction_id),
    created_at TIMESTAMPTZ NOT NULL,
    CHECK (original_transaction_id <> reversal_transaction_id)
);

CREATE TABLE pending_wager_references (
    transaction_id TEXT PRIMARY KEY REFERENCES wager_transactions(transaction_id),
    correlation_id TEXT NOT NULL,
    attempt_count INTEGER NOT NULL DEFAULT 0 CHECK (attempt_count >= 0),
    max_attempts INTEGER NOT NULL CHECK (max_attempts > 0),
    first_pending_at TIMESTAMPTZ NOT NULL,
    last_attempt_at TIMESTAMPTZ,
    next_attempt_at TIMESTAMPTZ NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL,
    completed_at TIMESTAMPTZ,
    CHECK (expires_at > first_pending_at),
    CHECK (next_attempt_at <= expires_at)
);
CREATE INDEX pending_wager_references_due_idx
    ON pending_wager_references(next_attempt_at, transaction_id) WHERE completed_at IS NULL;
