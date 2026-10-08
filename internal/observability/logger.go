package observability

import (
	"context"
	"io"
	"log/slog"
	"os"
	"strings"
)

// NewLogger creates a JSON structured logger configured by log level.
// Do not log secrets, tokens, or full financial payloads.
func NewLogger(level string) *slog.Logger {
	return NewLoggerToWriter(level, os.Stdout)
}

func NewLoggerToWriter(level string, writer io.Writer) *slog.Logger {
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

	handler := slog.NewJSONHandler(writer, &slog.HandlerOptions{
		Level:       logLevel,
		ReplaceAttr: safeAttribute,
	})

	return slog.New(handler)
}

func safeAttribute(_ []string, a slog.Attr) slog.Attr {
	key := strings.ToLower(a.Key)
	for _, sensitive := range []string{"authorization", "token", "secret", "password", "database_url", "databaseurl", "connectionstring", "dsn", "payload", "receiptHandle"} {
		if strings.Contains(key, strings.ToLower(sensitive)) {
			a.Value = slog.StringValue("[REDACTED]")
			return a
		}
	}
	if _, ok := a.Value.Any().(error); ok {
		a.Value = slog.StringValue("dependency_failure")
	}
	return a
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
