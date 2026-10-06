package ports

import (
	"context"
	"time"
)

type OutboxClaim struct {
	Event      OutboxEvent
	Sequence   int64
	ClaimedBy  string
	Token      string
	ClaimUntil time.Time
}

// PublicationStore performs short, committed SQL operations, never broker I/O.
// Tokens fence stale owners after a lease is recovered by another publisher.
// A caller supplies a fresh token for every Claim, even with the same owner ID.
type PublicationStore interface {
	Claim(context.Context, string, string, int, time.Duration) ([]OutboxClaim, error)
	Renew(context.Context, OutboxClaim, time.Duration) (bool, error)
	Published(context.Context, OutboxClaim) (bool, error)
	Retry(context.Context, OutboxClaim, time.Duration) (bool, error)
	Release(context.Context, OutboxClaim) (bool, error)
}

type EventSender interface {
	Send(context.Context, OutboxEvent) error
}
