DROP TABLE outbox_events;
DROP TABLE inbox_messages;
DROP TRIGGER wallet_ledger_append_only ON wallet_ledger_entries;
DROP TABLE wallet_ledger_entries;
DROP FUNCTION reject_ledger_mutation();
DROP TABLE wager_transactions;
DROP TABLE wallets;
