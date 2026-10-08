package ports

import (
	"context"
	"time"
)

// Telemetry records operational counts and elapsed time only. Monetary amounts
// and financial identifiers must never be passed as metric values or labels.
// Implementations must be concurrency-safe and non-blocking (no network I/O).
type Telemetry interface {
	Count(name string, labels ...string)
	Duration(name string, elapsed time.Duration, labels ...string)
	InFlight(name string, delta int)
}

type NoopTelemetry struct{}

func (NoopTelemetry) Count(string, ...string)                   {}
func (NoopTelemetry) Duration(string, time.Duration, ...string) {}
func (NoopTelemetry) InFlight(string, int)                      {}

type OperationalSnapshot struct {
	OutboxPending        int64
	OutboxRetryScheduled int64
	OutboxLag            time.Duration
	PendingReferences    int64
}

type OperationalReader interface {
	ReadOperationalSnapshot(ctx context.Context) (OperationalSnapshot, error)
}
