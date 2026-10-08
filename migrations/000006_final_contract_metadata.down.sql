DROP TRIGGER wager_terminal_immutable ON wager_transactions;
DROP FUNCTION reject_terminal_wager_mutation();
DROP INDEX wager_positive_opening_unique;
ALTER TABLE wager_transactions DROP COLUMN game_id;
