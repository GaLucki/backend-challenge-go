package application

import (
	"log/slog"
	"sync/atomic"

	"github.com/jackc/pgx/v5/pgxpool"
	httpadapter "github.com/junglegaming/backend-challenge-go/internal/adapter/http"
	"github.com/junglegaming/backend-challenge-go/internal/adapter/postgres"
	"github.com/junglegaming/backend-challenge-go/internal/config"
	"github.com/junglegaming/backend-challenge-go/internal/observability"
	"go.uber.org/fx"
)

// Module composes the application dependency graph.
var Module = fx.Options(
	fx.Provide(
		config.Load,
		newLogger,
		newReadyFlag,
		postgres.NewPool,
		func(pool *pgxpool.Pool) httpadapter.DatabasePinger { return pool },
		httpadapter.NewHealthHandler,
		httpadapter.NewHandler,
	),
	fx.Invoke(httpadapter.RegisterServer),
)

func newLogger(cfg config.Config) *slog.Logger {
	return observability.NewLogger(cfg.LogLevel)
}

func newReadyFlag() *atomic.Bool {
	return &atomic.Bool{}
}
