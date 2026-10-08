// Package outbox publishes already committed envelopes without financial logic.
package outbox

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/junglegaming/backend-challenge-go/internal/ports"
)

var ErrLeaseLost = errors.New("Outbox publication lease lost")

type Options struct {
	PublisherID                                 string
	PollInterval                                time.Duration
	BatchSize                                   int
	BaseRetryDelay, MaxRetryDelay               time.Duration
	ClaimDuration, PublishTimeout, StoreTimeout time.Duration
}

func (o Options) Validate() error {
	if o.PublisherID == "" || o.PollInterval <= 0 || o.BatchSize < 1 || o.BatchSize > 100 || o.BaseRetryDelay < time.Microsecond || o.MaxRetryDelay < o.BaseRetryDelay || o.PublishTimeout <= 0 || o.StoreTimeout <= 0 || o.ClaimDuration <= o.StoreTimeout || o.PublishTimeout >= o.ClaimDuration-o.StoreTimeout {
		return fmt.Errorf("invalid Outbox publisher options: lease must exceed publish and store deadlines")
	}
	return nil
}

// Backoff is bounded, saturating and independent of wall-clock/test scheduling.
func Backoff(base, maximum time.Duration, failedAttempt int32) time.Duration {
	delay := base
	for n := int32(1); n < failedAttempt && delay < maximum; n++ {
		if delay > maximum/2 {
			return maximum
		}
		delay *= 2
	}
	if delay > maximum {
		return maximum
	}
	return delay
}

type Publisher struct {
	store   ports.PublicationStore
	sender  ports.EventSender
	options Options
	logger  *slog.Logger
	metrics ports.Telemetry
}

func NewPublisher(store ports.PublicationStore, sender ports.EventSender, options Options, logger *slog.Logger) (*Publisher, error) {
	if err := options.Validate(); err != nil {
		return nil, err
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Publisher{store: store, sender: sender, options: options, logger: logger, metrics: ports.NoopTelemetry{}}, nil
}
func (p *Publisher) WithTelemetry(t ports.Telemetry) *Publisher {
	if t != nil {
		p.metrics = t
	}
	return p
}
func NewIdentity() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

// PublishClaim refreshes ownership before network I/O. SQL operations finish
// before Send, and success is recorded only after the sender confirms acceptance.
func (p *Publisher) PublishClaim(ctx context.Context, c ports.OutboxClaim) error {
	started := time.Now()
	defer func() { p.metrics.Duration("outbox_publication_duration_seconds", time.Since(started)) }()
	dbCtx, cancel := context.WithTimeout(ctx, p.options.StoreTimeout)
	owned, err := p.store.Renew(dbCtx, c, p.options.ClaimDuration)
	cancel()
	if err != nil {
		p.metrics.Count("outbox_events_total", "store_failure")
		p.metrics.Count("dependency_failures_total", "postgres", "outbox")
		return err
	}
	if !owned {
		p.log(ctx, c, "lease_lost", time.Since(started))
		return ErrLeaseLost
	}
	sendCtx, cancel := context.WithTimeout(ctx, p.options.PublishTimeout)
	err = p.sender.Send(sendCtx, c.Event)
	cancel()
	dbCtx, cancel = context.WithTimeout(ctx, p.options.StoreTimeout)
	defer cancel()
	if err != nil {
		p.metrics.Count("outbox_events_total", "publish_failure")
		p.metrics.Count("dependency_failures_total", "sqs", "outbox")
		attempt := c.Event.RetryCount
		if attempt < 1<<31-1 {
			attempt++
		}
		owned, saveErr := p.store.Retry(dbCtx, c, Backoff(p.options.BaseRetryDelay, p.options.MaxRetryDelay, attempt))
		if saveErr == nil && owned {
			p.metrics.Count("outbox_events_total", "retry_scheduled")
			c.Event.RetryCount = attempt
		}
		p.log(ctx, c, "publish_failed", time.Since(started))
		if saveErr != nil {
			p.metrics.Count("outbox_events_total", "store_failure")
			p.metrics.Count("dependency_failures_total", "postgres", "outbox")
			return saveErr
		}
		if !owned {
			p.log(ctx, c, "lease_lost", time.Since(started))
			return ErrLeaseLost
		}
		return err
	}
	owned, err = p.store.Published(dbCtx, c)
	if err != nil {
		p.metrics.Count("outbox_events_total", "mark_failure")
		p.metrics.Count("dependency_failures_total", "postgres", "outbox")
		p.log(ctx, c, "mark_failed", time.Since(started))
		return err
	}
	if !owned {
		p.log(ctx, c, "lease_lost", time.Since(started))
		return ErrLeaseLost
	}
	p.log(ctx, c, "published", time.Since(started))
	return nil
}
func (p *Publisher) log(ctx context.Context, c ports.OutboxClaim, result string, duration time.Duration) {
	if result == "published" {
		p.metrics.Count("outbox_events_total", "published")
	}
	if result == "lease_lost" {
		p.metrics.Count("outbox_events_total", "lease_lost")
	}
	var metadata struct {
		CorrelationID string `json:"correlationId"`
		CausationID   string `json:"causationId"`
		Data          struct {
			WalletID   string `json:"walletId"`
			ProviderID string `json:"providerId"`
			Type       string `json:"type"`
		} `json:"data"`
	}
	_ = json.Unmarshal(c.Event.Payload, &metadata)
	p.logger.InfoContext(ctx, "Outbox publication", "eventId", c.Event.EventID, "eventType", c.Event.EventType, "aggregateId", c.Event.AggregateID, "correlationId", metadata.CorrelationID, "transactionId", metadata.CausationID, "walletId", metadata.Data.WalletID, "providerId", metadata.Data.ProviderID, "operationType", metadata.Data.Type, "durationMs", duration.Milliseconds(), "retryCount", c.Event.RetryCount, "publisherId", p.options.PublisherID, "outcome", result)
}

// RunOnce claims a bounded batch of independent aggregate heads. Poll cancellation
// stops new sends; active items use the separate work context to drain safely.
func (p *Publisher) RunOnce(pollCtx, workCtx context.Context) (int, error) {
	if err := pollCtx.Err(); err != nil {
		return 0, err
	}
	token, err := NewIdentity()
	if err != nil {
		return 0, err
	}
	ctx, cancel := context.WithTimeout(pollCtx, p.options.StoreTimeout)
	claims, err := p.store.Claim(ctx, p.options.PublisherID, token, p.options.BatchSize, p.options.ClaimDuration)
	cancel()
	if err != nil {
		if pollCtx.Err() == nil {
			p.metrics.Count("outbox_events_total", "store_failure")
			p.metrics.Count("dependency_failures_total", "postgres", "outbox")
		}
		return 0, err
	}
	errs := make(chan error, len(claims))
	var wg sync.WaitGroup
	for _, c := range claims {
		wg.Add(1)
		go func(c ports.OutboxClaim) {
			defer wg.Done()
			if pollCtx.Err() != nil || workCtx.Err() != nil {
				// Release unsent claims concurrently so shutdown cleanup is bounded
				// by one store timeout rather than batch size times that timeout.
				cleanup, stop := context.WithTimeout(context.WithoutCancel(workCtx), p.options.StoreTimeout)
				_, err := p.store.Release(cleanup, c)
				stop()
				if err != nil {
					errs <- err
				}
				return
			}
			if err := p.PublishClaim(workCtx, c); err != nil {
				errs <- err
			}
		}(c)
	}
	wg.Wait()
	close(errs)
	var result error
	for err := range errs {
		result = errors.Join(result, err)
	}
	return len(claims), result
}
func (p *Publisher) Run(pollCtx, workCtx context.Context) {
	for pollCtx.Err() == nil && workCtx.Err() == nil {
		_, err := p.RunOnce(pollCtx, workCtx)
		if err != nil && pollCtx.Err() == nil && workCtx.Err() == nil {
			p.logger.WarnContext(workCtx, "Outbox batch incomplete", "publisherId", p.options.PublisherID)
		}
		timer := time.NewTimer(p.options.PollInterval)
		select {
		case <-pollCtx.Done():
			timer.Stop()
			return
		case <-workCtx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}
