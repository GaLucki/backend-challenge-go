package config

import (
	"strings"
	"testing"
)

func TestMetricsConfigurationAndSanitizedErrors(t *testing.T) {
	t.Setenv("APP_ENV", "test")
	t.Setenv("HTTP_PORT", "8080")
	t.Setenv("LOG_LEVEL", "info")
	t.Setenv("DATABASE_URL", "postgres://user:SECRET_PASSWORD@localhost/db")
	for _, tc := range []struct {
		enabled, interval, timeout string
		invalid                    bool
	}{{"true", "15s", "2s", false}, {"false", "15s", "2s", false}, {"invalid", "15s", "2s", true}, {"true", "0s", "2s", true}, {"true", "500ms", "100ms", true}, {"true", "1s", "2s", true}, {"true", "1s", "1s", true}, {"true", "1s", "bad-secret-duration", true}} {
		t.Setenv("METRICS_ENABLED", tc.enabled)
		t.Setenv("METRICS_COLLECT_INTERVAL", tc.interval)
		t.Setenv("METRICS_COLLECT_TIMEOUT", tc.timeout)
		_, err := Load()
		if (err != nil) != tc.invalid {
			t.Fatal(tc, err)
		}
		if err != nil && strings.Contains(err.Error(), "bad-secret-duration") {
			t.Fatal("invalid value leaked")
		}
	}
	if err := validateDatabaseURL("postgres://user:SECRET_PASSWORD@%zz/db"); err == nil || strings.Contains(err.Error(), "SECRET_PASSWORD") {
		t.Fatal("DSN leaked", err)
	}
}
