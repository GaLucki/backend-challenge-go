package config

import "testing"

func TestOutboxDefaultsAndInvalidConfig(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://user:pass@localhost:5432/db")
	t.Setenv("OUTBOX_PUBLISHER_ENABLED", "true")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.EventQueueName != "wager-events.fifo" || cfg.OutboxBatchSize != 10 {
		t.Fatal(cfg)
	}
	for _, tc := range []struct{ key, value string }{{"OUTBOX_PUBLISHER_ENABLED", "bad"}, {"OUTBOX_BATCH_SIZE", "0"}, {"OUTBOX_BATCH_SIZE", "101"}, {"OUTBOX_POLL_INTERVAL", "0s"}, {"OUTBOX_BASE_RETRY_DELAY", "1ns"}, {"OUTBOX_MAX_RETRY_DELAY", "1ns"}, {"OUTBOX_CLAIM_DURATION", "8s"}, {"OUTBOX_PUBLISH_TIMEOUT", "30s"}, {"OUTBOX_STORE_TIMEOUT", "0s"}, {"EVENT_QUEUE_NAME", "not-fifo"}, {"EVENT_QUEUE_NAME", "wager-transactions.fifo"}, {"EVENT_QUEUE_NAME", "wager-transactions-dlq.fifo"}, {"EVENT_QUEUE_URL", "file:///x"}, {"AWS_REGION", ""}} {
		t.Run(tc.key+tc.value, func(t *testing.T) {
			t.Setenv(tc.key, tc.value)
			if _, err := Load(); err == nil {
				t.Fatal("invalid publisher config accepted")
			}
		})
	}
}
