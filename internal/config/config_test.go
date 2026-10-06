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
	t.Setenv("DATABASE_URL", "postgres://wagering:wagering@localhost:5432/wagering?sslmode=disable")

	_, err := Load()
	if err == nil {
		t.Fatal("expected error for empty APP_ENV")
	}
}

func TestLoadWithValidValues(t *testing.T) {
	t.Setenv("APP_ENV", "development")
	t.Setenv("HTTP_PORT", "8080")
	t.Setenv("LOG_LEVEL", "INFO")
	t.Setenv("DATABASE_URL", "postgres://wagering:wagering@localhost:5432/wagering?sslmode=disable")
	t.Setenv("DB_MAX_CONNS", "20")
	t.Setenv("DB_MIN_CONNS", "2")
	t.Setenv("DB_MAX_CONN_LIFETIME", "45m")
	t.Setenv("DB_MAX_CONN_IDLE_TIME", "10m")
	t.Setenv("DB_CONNECT_TIMEOUT", "3s")
	t.Setenv("DB_HEALTH_TIMEOUT", "1s")
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
	if cfg.DatabaseURL != "postgres://wagering:wagering@localhost:5432/wagering?sslmode=disable" {
		t.Fatalf("DatabaseURL was not loaded")
	}
	if cfg.DBMaxConns != 20 {
		t.Fatalf("DBMaxConns = %d, want 20", cfg.DBMaxConns)
	}
	if cfg.DBMinConns != 2 {
		t.Fatalf("DBMinConns = %d, want 2", cfg.DBMinConns)
	}
	if cfg.DBMaxConnLifetime != 45*time.Minute {
		t.Fatalf("DBMaxConnLifetime = %v, want 45m", cfg.DBMaxConnLifetime)
	}
	if cfg.DBMaxConnIdleTime != 10*time.Minute {
		t.Fatalf("DBMaxConnIdleTime = %v, want 10m", cfg.DBMaxConnIdleTime)
	}
	if cfg.DBConnectTimeout != 3*time.Second {
		t.Fatalf("DBConnectTimeout = %v, want 3s", cfg.DBConnectTimeout)
	}
	if cfg.DBHealthTimeout != time.Second {
		t.Fatalf("DBHealthTimeout = %v, want 1s", cfg.DBHealthTimeout)
	}
	if cfg.Addr() != ":8080" {
		t.Fatalf("Addr() = %q, want :8080", cfg.Addr())
	}
}

func TestLoadUsesDefaultsWhenUnset(t *testing.T) {
	t.Setenv("APP_ENV", "development")
	t.Setenv("DATABASE_URL", "postgres://wagering:wagering@localhost:5432/wagering?sslmode=disable")
	unsetEnv(t, "HTTP_PORT")
	unsetEnv(t, "LOG_LEVEL")
	unsetEnv(t, "SHUTDOWN_TIMEOUT")
	unsetEnv(t, "DB_MAX_CONNS")
	unsetEnv(t, "DB_MIN_CONNS")
	unsetEnv(t, "DB_MAX_CONN_LIFETIME")
	unsetEnv(t, "DB_MAX_CONN_IDLE_TIME")
	unsetEnv(t, "DB_CONNECT_TIMEOUT")
	unsetEnv(t, "DB_HEALTH_TIMEOUT")

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
	if cfg.DBMaxConns != defaultDBMaxConns {
		t.Fatalf("DBMaxConns = %d, want %d", cfg.DBMaxConns, defaultDBMaxConns)
	}
	if cfg.DBMinConns != defaultDBMinConns {
		t.Fatalf("DBMinConns = %d, want %d", cfg.DBMinConns, defaultDBMinConns)
	}
}

func TestLoadRejectsMissingDatabaseURL(t *testing.T) {
	t.Setenv("APP_ENV", "development")
	t.Setenv("HTTP_PORT", "8080")
	t.Setenv("LOG_LEVEL", "info")
	unsetEnv(t, "DATABASE_URL")

	_, err := Load()
	if err == nil {
		t.Fatal("expected error for missing DATABASE_URL")
	}
}

func TestLoadRejectsInvalidDatabaseURLScheme(t *testing.T) {
	t.Setenv("APP_ENV", "development")
	t.Setenv("HTTP_PORT", "8080")
	t.Setenv("LOG_LEVEL", "info")
	t.Setenv("DATABASE_URL", "mysql://localhost:3306/wagering")

	_, err := Load()
	if err == nil {
		t.Fatal("expected error for invalid DATABASE_URL scheme")
	}
}

func TestValidateRejectsInvalidPort(t *testing.T) {
	cfg := Config{
		AppEnv:            "development",
		HTTPPort:          "70000",
		LogLevel:          "info",
		ShutdownTimeout:   time.Second,
		DatabaseURL:       "postgres://wagering:wagering@localhost:5432/wagering?sslmode=disable",
		DBMaxConns:        10,
		DBMinConns:        1,
		DBMaxConnLifetime: time.Hour,
		DBMaxConnIdleTime: 30 * time.Minute,
		DBConnectTimeout:  5 * time.Second,
		DBHealthTimeout:   2 * time.Second,
	}

	if err := cfg.Validate(); err == nil {
		t.Fatal("expected invalid port error")
	}
}

func TestValidateRejectsInvalidLogLevel(t *testing.T) {
	cfg := Config{
		AppEnv:            "development",
		HTTPPort:          "8080",
		LogLevel:          "verbose",
		ShutdownTimeout:   time.Second,
		DatabaseURL:       "postgres://wagering:wagering@localhost:5432/wagering?sslmode=disable",
		DBMaxConns:        10,
		DBMinConns:        1,
		DBMaxConnLifetime: time.Hour,
		DBMaxConnIdleTime: 30 * time.Minute,
		DBConnectTimeout:  5 * time.Second,
		DBHealthTimeout:   2 * time.Second,
	}

	if err := cfg.Validate(); err == nil {
		t.Fatal("expected invalid log level error")
	}
}

func TestValidateRejectsMinConnsGreaterThanMax(t *testing.T) {
	cfg := Config{
		AppEnv:            "development",
		HTTPPort:          "8080",
		LogLevel:          "info",
		ShutdownTimeout:   time.Second,
		DatabaseURL:       "postgres://wagering:wagering@localhost:5432/wagering?sslmode=disable",
		DBMaxConns:        2,
		DBMinConns:        5,
		DBMaxConnLifetime: time.Hour,
		DBMaxConnIdleTime: 30 * time.Minute,
		DBConnectTimeout:  5 * time.Second,
		DBHealthTimeout:   2 * time.Second,
	}

	if err := cfg.Validate(); err == nil {
		t.Fatal("expected min/max conns validation error")
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
