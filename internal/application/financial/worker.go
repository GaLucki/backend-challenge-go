package financial

import (
	"context"
	"fmt"
	"log/slog"
	"time"
)

type WorkerOptions struct {
	PollInterval time.Duration
	BatchSize    int
}
type PendingWorker struct {
	service *Service
	options WorkerOptions
	logger  *slog.Logger
}

func NewPendingWorker(service *Service, options WorkerOptions, logger *slog.Logger) (*PendingWorker, error) {
	if options.PollInterval <= 0 || options.BatchSize < 1 {
		return nil, fmt.Errorf("invalid pending worker options")
	}
	return &PendingWorker{service: service, options: options, logger: logger}, nil
}
func (w *PendingWorker) RunOnce(ctx context.Context) (int, error) {
	at := w.service.now()
	count := 0
	for count < w.options.BatchSize {
		if err := ctx.Err(); err != nil {
			return count, err
		}
		handled, err := w.service.ResolvePendingOnce(ctx, at)
		if err != nil {
			return count, err
		}
		if !handled {
			break
		}
		count++
	}
	return count, nil
}

// Run owns no child goroutines; Fx owns this loop and waits for cancellation.
func (w *PendingWorker) Run(ctx context.Context) {
	ticker := time.NewTicker(w.options.PollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if _, err := w.RunOnce(ctx); err != nil && ctx.Err() == nil {
				w.logger.ErrorContext(ctx, "pending reference batch failed", "error", err)
			}
		}
	}
}
