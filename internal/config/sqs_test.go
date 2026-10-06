package config

import (
	"testing"
	"time"
)

func TestSQSConfiguration(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://user:pass@localhost:5432/db")
	t.Setenv("SQS_ENABLED", "true")
	t.Setenv("SQS_ENDPOINT", "http://localhost:4566")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.SQSWaitSeconds != 20 || cfg.SQSVisibilitySeconds != 90 || cfg.SQSMaxMessages != 10 || cfg.SQSConcurrency != 4 {
		t.Fatal("invalid defaults")
	}
}

func TestSQSBudgetAndCredentials(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://user:pass@localhost:5432/db")
	t.Setenv("SQS_ENABLED", "true")
	t.Setenv("AWS_ACCESS_KEY_ID", "test")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "test")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	cfg.SQSProcessingTimeout = time.Duration(1<<63 - 1)
	if cfg.Validate() == nil {
		t.Fatal("overflowing visibility budget accepted")
	}
	cfg.SQSProcessingTimeout = 5 * time.Second
	cfg.AWSSecretAccessKey = ""
	if cfg.Validate() == nil {
		t.Fatal("incomplete credentials accepted")
	}
}
func TestInvalidSQSConfiguration(t *testing.T) {
	for _, tc := range []struct{ key, value string }{{"SQS_ENABLED", "invalid"}, {"SQS_QUEUE_NAME", "not-fifo"}, {"SQS_CONSUMER_NAME", ""}, {"AWS_REGION", ""}, {"SQS_ENDPOINT", "file:///x"}, {"SQS_QUEUE_URL", "bad"}, {"SQS_WAIT_SECONDS", "0"}, {"SQS_WAIT_SECONDS", "21"}, {"SQS_MAX_MESSAGES", "11"}, {"SQS_CONCURRENCY", "0"}, {"SQS_VISIBILITY_SECONDS", "10"}, {"SQS_PROCESSING_TIMEOUT", "0s"}} {
		t.Run(tc.key+tc.value, func(t *testing.T) {
			t.Setenv("DATABASE_URL", "postgres://user:pass@localhost:5432/db")
			t.Setenv("SQS_ENABLED", "true")
			t.Setenv(tc.key, tc.value)
			if _, err := Load(); err == nil {
				t.Fatal("invalid SQS configuration accepted")
			}
		})
	}
}
