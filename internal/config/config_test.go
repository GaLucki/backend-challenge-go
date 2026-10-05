package config

import (
	"os"
	"testing"
	"time"
)

func TestLoadRejectsEmptyAppEnv(t *testing.T) {
	t.Setenv("APP_ENV", "")
	t.Setenv("HTTP_PORT", "8080")
	t.Setenv("LOG_LEVEL", "info")

	_, err := Load()
	if err == nil {
		t.Fatal("expected error for empty APP_ENV")
	}
}

func TestLoadWithValidValues(t *testing.T) {
	t.Setenv("APP_ENV", "development")
	t.Setenv("HTTP_PORT", "8080")
	t.Setenv("LOG_LEVEL", "INFO")
	t.Setenv("DATABASE_URL", "postgres://example")
	t.Setenv("KEYCLOAK_URL", "http://localhost:8081")
	t.Setenv("SQS_ENDPOINT", "http://localhost:4566")
	t.Setenv("AWS_REGION", "us-east-1")
	t.Setenv("AWS_ACCESS_KEY_ID", "test")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "test")
	t.Setenv("SHUTDOWN_TIMEOUT", "5s")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() unexpected error: %v", err)
	}

	if cfg.AppEnv != "development" {
		t.Fatalf("AppEnv = %q, want development", cfg.AppEnv)
	}
	if cfg.HTTPPort != "8080" {
		t.Fatalf("HTTPPort = %q, want 8080", cfg.HTTPPort)
	}
	if cfg.LogLevel != "info" {
		t.Fatalf("LogLevel = %q, want info", cfg.LogLevel)
	}
	if cfg.ShutdownTimeout != 5*time.Second {
		t.Fatalf("ShutdownTimeout = %v, want 5s", cfg.ShutdownTimeout)
	}
	if cfg.DatabaseURL != "postgres://example" {
		t.Fatalf("DatabaseURL was not loaded")
	}
	if cfg.Addr() != ":8080" {
		t.Fatalf("Addr() = %q, want :8080", cfg.Addr())
	}
}

func TestLoadUsesDefaultsWhenUnset(t *testing.T) {
	t.Setenv("APP_ENV", "development")
	unsetEnv(t, "HTTP_PORT")
	unsetEnv(t, "LOG_LEVEL")
	unsetEnv(t, "SHUTDOWN_TIMEOUT")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() unexpected error: %v", err)
	}

	if cfg.HTTPPort != defaultHTTPPort {
		t.Fatalf("HTTPPort = %q, want %q", cfg.HTTPPort, defaultHTTPPort)
	}
	if cfg.LogLevel != defaultLogLevel {
		t.Fatalf("LogLevel = %q, want %q", cfg.LogLevel, defaultLogLevel)
	}
	if cfg.ShutdownTimeout != defaultShutdownTimeout {
		t.Fatalf("ShutdownTimeout = %v, want %v", cfg.ShutdownTimeout, defaultShutdownTimeout)
	}
}

func TestValidateRejectsInvalidPort(t *testing.T) {
	cfg := Config{
		AppEnv:          "development",
		HTTPPort:        "70000",
		LogLevel:        "info",
		ShutdownTimeout: time.Second,
	}

	if err := cfg.Validate(); err == nil {
		t.Fatal("expected invalid port error")
	}
}

func TestValidateRejectsInvalidLogLevel(t *testing.T) {
	cfg := Config{
		AppEnv:          "development",
		HTTPPort:        "8080",
		LogLevel:        "verbose",
		ShutdownTimeout: time.Second,
	}

	if err := cfg.Validate(); err == nil {
		t.Fatal("expected invalid log level error")
	}
}

func unsetEnv(t *testing.T, key string) {
	t.Helper()
	original, existed := os.LookupEnv(key)
	if err := os.Unsetenv(key); err != nil {
		t.Fatalf("Unsetenv(%q): %v", key, err)
	}
	t.Cleanup(func() {
		if existed {
			_ = os.Setenv(key, original)
			return
		}
		_ = os.Unsetenv(key)
	})
}
