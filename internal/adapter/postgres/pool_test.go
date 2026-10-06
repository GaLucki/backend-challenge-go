package postgres

import (
	"testing"
	"time"

	"github.com/junglegaming/backend-challenge-go/internal/config"
)

func TestBuildPoolConfigAppliesSettings(t *testing.T) {
	cfg := config.Config{
		DatabaseURL:       "postgres://wagering:wagering@localhost:5432/wagering?sslmode=disable",
		DBMaxConns:        12,
		DBMinConns:        2,
		DBMaxConnLifetime: 45 * time.Minute,
		DBMaxConnIdleTime: 15 * time.Minute,
		DBConnectTimeout:  3 * time.Second,
	}

	poolCfg, err := BuildPoolConfig(cfg)
	if err != nil {
		t.Fatalf("BuildPoolConfig() error = %v", err)
	}

	if poolCfg.MaxConns != 12 {
		t.Fatalf("MaxConns = %d, want 12", poolCfg.MaxConns)
	}
	if poolCfg.MinConns != 2 {
		t.Fatalf("MinConns = %d, want 2", poolCfg.MinConns)
	}
	if poolCfg.MaxConnLifetime != 45*time.Minute {
		t.Fatalf("MaxConnLifetime = %v, want 45m", poolCfg.MaxConnLifetime)
	}
	if poolCfg.MaxConnIdleTime != 15*time.Minute {
		t.Fatalf("MaxConnIdleTime = %v, want 15m", poolCfg.MaxConnIdleTime)
	}
	if poolCfg.ConnConfig.ConnectTimeout != 3*time.Second {
		t.Fatalf("ConnectTimeout = %v, want 3s", poolCfg.ConnConfig.ConnectTimeout)
	}
	if poolCfg.ConnConfig.Host != "localhost" {
		t.Fatalf("Host = %q, want localhost", poolCfg.ConnConfig.Host)
	}
	if poolCfg.ConnConfig.Database != "wagering" {
		t.Fatalf("Database = %q, want wagering", poolCfg.ConnConfig.Database)
	}
}

func TestBuildPoolConfigRejectsInvalidURL(t *testing.T) {
	_, err := BuildPoolConfig(config.Config{
		DatabaseURL: "://bad",
	})
	if err == nil {
		t.Fatal("expected error for invalid DATABASE_URL")
	}
}
