package application

import (
	"context"
	"log/slog"

	"github.com/junglegaming/backend-challenge-go/internal/application/financial"
	"github.com/junglegaming/backend-challenge-go/internal/config"
	"github.com/junglegaming/backend-challenge-go/internal/observability"
	"github.com/junglegaming/backend-challenge-go/internal/ports"
	"go.uber.org/fx"
)

func newFinancialService(uow ports.UnitOfWork, cfg config.Config, metrics *observability.Metrics, logger *slog.Logger) (*financial.Service, error) {
	s, err := financial.NewServiceWithPolicy(uow, financial.PendingPolicy{BaseDelay: cfg.PendingBaseDelay, MaxDelay: cfg.PendingMaxDelay, MaxAttempts: cfg.PendingMaxAttempts, TTL: cfg.PendingTTL})
	if err != nil {
		return nil, err
	}
	return s.WithTelemetry(metrics, logger), nil
}
func newPendingWorker(service *financial.Service, cfg config.Config, logger *slog.Logger) (*financial.PendingWorker, error) {
	return financial.NewPendingWorker(service, financial.WorkerOptions{PollInterval: cfg.PendingPollInterval, BatchSize: int(cfg.PendingBatchSize)}, logger)
}
func RegisterPendingWorker(lc fx.Lifecycle, worker *financial.PendingWorker) {
	var cancel context.CancelFunc
	done := make(chan struct{})
	lc.Append(fx.Hook{
		OnStart: func(context.Context) error {
			var runCtx context.Context
			runCtx, cancel = context.WithCancel(context.Background())
			go func() { defer close(done); worker.Run(runCtx) }()
			return nil
		},
		OnStop: func(ctx context.Context) error {
			cancel()
			select {
			case <-done:
				return nil
			case <-ctx.Done():
				<-done
				return ctx.Err()
			}
		},
	})
}
