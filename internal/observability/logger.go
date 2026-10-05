package observability

import (
	"context"
	"log/slog"
	"os"
	"strings"
)

// NewLogger creates a JSON structured logger configured by log level.
// Do not log secrets, tokens, or full financial payloads.
func NewLogger(level string) *slog.Logger {
	var logLevel slog.Level
	switch strings.ToLower(level) {
	case "debug":
		logLevel = slog.LevelDebug
	case "warn":
		logLevel = slog.LevelWarn
	case "error":
		logLevel = slog.LevelError
	default:
		logLevel = slog.LevelInfo
	}

	handler := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: logLevel,
	})

	return slog.New(handler)
}

// LoggerWithContext returns a logger enriched with identifiers from the context.
func LoggerWithContext(ctx context.Context, logger *slog.Logger) *slog.Logger {
	if logger == nil {
		logger = slog.Default()
	}

	fields := FieldsFromContext(ctx)
	if len(fields) == 0 {
		return logger
	}

	attrs := make([]any, 0, len(fields)*2)
	for key, value := range fields {
		attrs = append(attrs, key, value)
	}

	return logger.With(attrs...)
}
