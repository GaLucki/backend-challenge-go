package postgres

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/junglegaming/backend-challenge-go/internal/config"
	"go.uber.org/fx"
)

// BuildPoolConfig parses DATABASE_URL and applies pool settings from Config.
func BuildPoolConfig(cfg config.Config) (*pgxpool.Config, error) {
	poolCfg, err := pgxpool.ParseConfig(cfg.DatabaseURL)
	if err != nil {
		return nil, fmt.Errorf("parse DATABASE_URL failed")
	}

	poolCfg.MaxConns = cfg.DBMaxConns
	poolCfg.MinConns = cfg.DBMinConns
	poolCfg.MaxConnLifetime = cfg.DBMaxConnLifetime
	poolCfg.MaxConnIdleTime = cfg.DBMaxConnIdleTime
	poolCfg.ConnConfig.ConnectTimeout = cfg.DBConnectTimeout

	return poolCfg, nil
}

// NewPool creates a pgx connection pool and registers Fx lifecycle hooks.
// The pool is pinged on start and closed on stop. Migrations are not applied here.
func NewPool(lc fx.Lifecycle, cfg config.Config, logger *slog.Logger) (*pgxpool.Pool, error) {
	poolCfg, err := BuildPoolConfig(cfg)
	if err != nil {
		return nil, err
	}

	pool, err := pgxpool.NewWithConfig(context.Background(), poolCfg)
	if err != nil {
		return nil, fmt.Errorf("create postgres pool failed")
	}

	lc.Append(fx.Hook{
		OnStart: func(ctx context.Context) error {
			pingCtx, cancel := context.WithTimeout(ctx, cfg.DBConnectTimeout)
			defer cancel()

			if err := pool.Ping(pingCtx); err != nil {
				pool.Close()
				return fmt.Errorf("postgres ping failed")
			}

			logger.InfoContext(ctx, "postgres pool ready",
				"maxConns", cfg.DBMaxConns,
				"minConns", cfg.DBMinConns,
			)
			return nil
		},
		OnStop: func(ctx context.Context) error {
			logger.InfoContext(ctx, "closing postgres pool")
			pool.Close()
			return nil
		},
	})

	return pool, nil
}
