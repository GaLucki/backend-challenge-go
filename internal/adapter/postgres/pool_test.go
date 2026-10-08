package postgres

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/jackc/puddle/v2"
	"github.com/junglegaming/backend-challenge-go/internal/config"
	"go.uber.org/fx"
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

type recordedPoolLifecycle struct {
	hooks []fx.Hook
}

func (l *recordedPoolLifecycle) Append(hook fx.Hook) {
	l.hooks = append(l.hooks, hook)
}

func TestPoolFailedStartClosesPoolAndSanitizesError(t *testing.T) {
	lifecycle := &recordedPoolLifecycle{}
	pool, err := NewPool(lifecycle, config.Config{
		DatabaseURL:      "postgres://wagering:PRIVATE_PASSWORD@localhost:5432/wagering?sslmode=disable",
		DBMaxConns:       1,
		DBConnectTimeout: time.Second,
	}, slog.New(slog.NewJSONHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if len(lifecycle.hooks) != 1 {
		t.Fatalf("expected one pool lifecycle hook, got %d", len(lifecycle.hooks))
	}
	err = lifecycle.hooks[0].OnStart(ctx)
	if err == nil || strings.Contains(err.Error(), "PRIVATE_PASSWORD") {
		t.Fatalf("expected sanitized startup failure, got %v", err)
	}
	// Fx does not run OnStop for a hook whose OnStart failed. A fresh context
	// must therefore see a closed pool without invoking its OnStop hook.
	acquireCtx, acquireCancel := context.WithTimeout(context.Background(), time.Second)
	defer acquireCancel()
	conn, err := pool.Acquire(acquireCtx)
	if conn != nil {
		conn.Release()
	}
	if !errors.Is(err, puddle.ErrClosedPool) {
		t.Fatalf("pool remained open after failed startup: %v", err)
	}
}

func TestBuildPoolConfigDoesNotExposeCredentials(t *testing.T) {
	_, err := BuildPoolConfig(config.Config{
		DatabaseURL: "postgres://wagering:PRIVATE_PASSWORD@localhost:INVALID_PORT/wagering",
	})
	if err == nil || strings.Contains(err.Error(), "PRIVATE_PASSWORD") {
		t.Fatalf("expected sanitized configuration error, got %v", err)
	}
}
