package config

import (
	"testing"
	"time"
)

func TestPendingReferenceConfiguration(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://user:pass@localhost:5432/database")
	keys := []string{"PENDING_REFERENCE_POLL_INTERVAL", "PENDING_REFERENCE_BASE_DELAY", "PENDING_REFERENCE_MAX_DELAY", "PENDING_REFERENCE_TTL", "PENDING_REFERENCE_MAX_ATTEMPTS", "PENDING_REFERENCE_BATCH_SIZE"}
	for _, key := range keys {
		unsetEnv(t, key)
	}
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.PendingPollInterval != time.Second || cfg.PendingBaseDelay != time.Second || cfg.PendingMaxDelay != time.Minute || cfg.PendingTTL != 15*time.Minute || cfg.PendingMaxAttempts != 10 || cfg.PendingBatchSize != 50 {
		t.Fatalf("defaults: %+v", cfg)
	}
	for key, value := range map[string]string{"PENDING_REFERENCE_POLL_INTERVAL": "2s", "PENDING_REFERENCE_BASE_DELAY": "3s", "PENDING_REFERENCE_MAX_DELAY": "10s", "PENDING_REFERENCE_TTL": "1h", "PENDING_REFERENCE_MAX_ATTEMPTS": "7", "PENDING_REFERENCE_BATCH_SIZE": "12"} {
		t.Setenv(key, value)
	}
	cfg, err = Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.PendingPollInterval != 2*time.Second || cfg.PendingBaseDelay != 3*time.Second || cfg.PendingMaxDelay != 10*time.Second || cfg.PendingTTL != time.Hour || cfg.PendingMaxAttempts != 7 || cfg.PendingBatchSize != 12 {
		t.Fatalf("overrides: %+v", cfg)
	}
}
func TestInvalidPendingReferenceConfiguration(t *testing.T) {
	for _, tc := range []struct{ key, value string }{
		{"PENDING_REFERENCE_BASE_DELAY", "1ns"}, {"PENDING_REFERENCE_TTL", "1ns"},
		{"PENDING_REFERENCE_POLL_INTERVAL", "0s"}, {"PENDING_REFERENCE_BASE_DELAY", "-1s"}, {"PENDING_REFERENCE_MAX_DELAY", "0s"}, {"PENDING_REFERENCE_TTL", "nonsense"},
		{"PENDING_REFERENCE_MAX_ATTEMPTS", "0"}, {"PENDING_REFERENCE_MAX_ATTEMPTS", "-1"}, {"PENDING_REFERENCE_BATCH_SIZE", "0"}, {"PENDING_REFERENCE_BATCH_SIZE", "invalid"}, {"PENDING_REFERENCE_MAX_DELAY", "1ms"},
	} {
		t.Run(tc.key+tc.value, func(t *testing.T) {
			t.Setenv("DATABASE_URL", "postgres://user:pass@localhost:5432/database")
			t.Setenv(tc.key, tc.value)
			if _, err := Load(); err == nil {
				t.Fatal("invalid configuration accepted")
			}
		})
	}
}
