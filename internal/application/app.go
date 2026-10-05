package application

import (
	"log/slog"
	"sync/atomic"

	httpadapter "github.com/junglegaming/backend-challenge-go/internal/adapter/http"
	"github.com/junglegaming/backend-challenge-go/internal/config"
	"github.com/junglegaming/backend-challenge-go/internal/observability"
	"go.uber.org/fx"
)

// Module composes the phase-0 application graph.
// Future phases should register additional fx.Module options here.
var Module = fx.Options(
	fx.Provide(
		config.Load,
		newLogger,
		newReadyFlag,
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
