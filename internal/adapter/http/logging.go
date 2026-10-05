package httpadapter

import (
	"context"
	"log/slog"

	"github.com/junglegaming/backend-challenge-go/internal/observability"
)

func observabilityLogger(ctx context.Context, logger *slog.Logger) *slog.Logger {
	return observability.LoggerWithContext(ctx, logger)
}
