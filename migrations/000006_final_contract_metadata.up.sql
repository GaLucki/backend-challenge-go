-- Preserve historical records and their hashes. Older protocol versions did not
-- carry game_id, so NULL explicitly denotes unknown historical metadata.
ALTER TABLE wager_transactions ADD COLUMN game_id TEXT
    CHECK (game_id IS NULL OR btrim(game_id) <> '');
CREATE UNIQUE INDEX wager_positive_opening_unique ON wager_transactions(wallet_id)
    WHERE type = 'OPENING' AND amount_cents > 0;

CREATE FUNCTION reject_terminal_wager_mutation() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        IF OLD.state IN ('PROCESSED', 'REJECTED', 'FAILED') THEN
            RAISE EXCEPTION 'terminal wager is immutable' USING ERRCODE = '23514';
        END IF;
        RETURN OLD;
    END IF;
    IF OLD.state IN ('PROCESSED', 'REJECTED', 'FAILED') AND NEW IS DISTINCT FROM OLD THEN
        RAISE EXCEPTION 'terminal wager is immutable' USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER wager_terminal_immutable BEFORE UPDATE OR DELETE ON wager_transactions
    FOR EACH ROW EXECUTE FUNCTION reject_terminal_wager_mutation();
