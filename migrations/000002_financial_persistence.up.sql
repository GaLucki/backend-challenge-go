CREATE TABLE wallets (
    id TEXT PRIMARY KEY CHECK (btrim(id) <> ''),
    player_id TEXT NOT NULL CHECK (btrim(player_id) <> ''),
    currency TEXT NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
    balance_cents BIGINT NOT NULL CHECK (balance_cents >= 0),
    version BIGINT NOT NULL CHECK (version >= 1),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (player_id, currency),
    UNIQUE (id, player_id, currency)
);

CREATE TABLE wager_transactions (
    transaction_id TEXT PRIMARY KEY CHECK (btrim(transaction_id) <> ''),
    provider_id TEXT,
    external_transaction_id TEXT,
    player_id TEXT NOT NULL,
    wallet_id TEXT NOT NULL REFERENCES wallets(id),
    currency TEXT NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
    type TEXT NOT NULL CHECK (type IN ('OPENING', 'BET', 'WIN', 'LOSS', 'REFUND', 'ROLLBACK')),
    amount_cents BIGINT NOT NULL CHECK (amount_cents >= 0),
    round_id TEXT,
    reference_external_transaction_id TEXT,
    state TEXT NOT NULL CHECK (state IN ('PENDING', 'PENDING_REFERENCE', 'PROCESSED', 'REJECTED', 'FAILED')),
    failure_code TEXT,
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL,
    UNIQUE (provider_id, external_transaction_id),
    UNIQUE (wallet_id, transaction_id),
    FOREIGN KEY (wallet_id, player_id, currency) REFERENCES wallets(id, player_id, currency),
    CHECK ((type = 'OPENING' AND provider_id IS NULL AND external_transaction_id IS NULL AND round_id IS NULL AND reference_external_transaction_id IS NULL AND state = 'PROCESSED')
        OR (type <> 'OPENING' AND provider_id IS NOT NULL AND btrim(provider_id) <> ''
            AND external_transaction_id IS NOT NULL AND btrim(external_transaction_id) <> ''
            AND round_id IS NOT NULL AND btrim(round_id) <> ''))
);
CREATE INDEX wager_transactions_wallet_idx ON wager_transactions(wallet_id);
CREATE INDEX wager_transactions_state_idx ON wager_transactions(state);
CREATE INDEX wager_transactions_reference_idx ON wager_transactions(provider_id, reference_external_transaction_id)
    WHERE reference_external_transaction_id IS NOT NULL;

CREATE TABLE wallet_ledger_entries (
    id TEXT PRIMARY KEY CHECK (btrim(id) <> ''),
    wallet_id TEXT NOT NULL REFERENCES wallets(id),
    transaction_id TEXT NOT NULL REFERENCES wager_transactions(transaction_id),
    direction TEXT NOT NULL CHECK (direction IN ('CREDIT', 'DEBIT')),
    amount_cents BIGINT NOT NULL CHECK (amount_cents > 0),
    balance_before_cents BIGINT NOT NULL CHECK (balance_before_cents >= 0),
    balance_after_cents BIGINT NOT NULL CHECK (balance_after_cents >= 0),
    wallet_version BIGINT NOT NULL CHECK (wallet_version >= 1),
    created_at TIMESTAMPTZ NOT NULL,
    UNIQUE (wallet_id, transaction_id),
    FOREIGN KEY (wallet_id, transaction_id) REFERENCES wager_transactions(wallet_id, transaction_id),
    CHECK ((direction = 'CREDIT' AND balance_after_cents::numeric = balance_before_cents::numeric + amount_cents::numeric)
        OR (direction = 'DEBIT' AND balance_after_cents::numeric = balance_before_cents::numeric - amount_cents::numeric))
);

-- Statement triggers also reject empty UPDATE/DELETE and TRUNCATE. Inserts remain allowed.
CREATE FUNCTION reject_ledger_mutation() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'wallet ledger is append-only' USING ERRCODE = '23514';
END;
$$;
CREATE TRIGGER wallet_ledger_append_only BEFORE UPDATE OR DELETE OR TRUNCATE
    ON wallet_ledger_entries FOR EACH STATEMENT EXECUTE FUNCTION reject_ledger_mutation();

CREATE TABLE inbox_messages (
    consumer_name TEXT NOT NULL CHECK (btrim(consumer_name) <> ''),
    message_id TEXT NOT NULL CHECK (btrim(message_id) <> ''),
    payload_hash TEXT NOT NULL CHECK (payload_hash ~ '^[0-9a-f]{64}$'),
    received_at TIMESTAMPTZ NOT NULL,
    completed_at TIMESTAMPTZ,
    PRIMARY KEY (consumer_name, message_id)
);

CREATE TABLE outbox_events (
    event_id TEXT PRIMARY KEY CHECK (btrim(event_id) <> ''),
    aggregate_id TEXT NOT NULL CHECK (btrim(aggregate_id) <> ''),
    event_type TEXT NOT NULL CHECK (btrim(event_type) <> ''),
    payload JSONB NOT NULL,
    occurred_at TIMESTAMPTZ NOT NULL,
    retry_count INTEGER NOT NULL DEFAULT 0 CHECK (retry_count >= 0),
    next_attempt_at TIMESTAMPTZ NOT NULL,
    published_at TIMESTAMPTZ
);
-- Future publishers must select these rows FOR UPDATE SKIP LOCKED inside a transaction.
CREATE INDEX outbox_events_ready_idx ON outbox_events(next_attempt_at, event_id) WHERE published_at IS NULL;
