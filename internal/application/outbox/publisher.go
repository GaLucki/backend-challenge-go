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
}

func NewPublisher(store ports.PublicationStore, sender ports.EventSender, options Options, logger *slog.Logger) (*Publisher, error) {
	if err := options.Validate(); err != nil {
		return nil, err
	}
	return &Publisher{store: store, sender: sender, options: options, logger: logger}, nil
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
	dbCtx, cancel := context.WithTimeout(ctx, p.options.StoreTimeout)
	owned, err := p.store.Renew(dbCtx, c, p.options.ClaimDuration)
	cancel()
	if err != nil {
		return err
	}
	if !owned {
		p.log(ctx, c, "lease_lost")
		return ErrLeaseLost
	}
	sendCtx, cancel := context.WithTimeout(ctx, p.options.PublishTimeout)
	err = p.sender.Send(sendCtx, c.Event)
	cancel()
	dbCtx, cancel = context.WithTimeout(ctx, p.options.StoreTimeout)
	defer cancel()
	if err != nil {
		attempt := c.Event.RetryCount
		if attempt < 1<<31-1 {
			attempt++
		}
		owned, saveErr := p.store.Retry(dbCtx, c, Backoff(p.options.BaseRetryDelay, p.options.MaxRetryDelay, attempt))
		if saveErr == nil && owned {
			c.Event.RetryCount = attempt
		}
		p.log(ctx, c, "publish_failed")
		if saveErr != nil {
			return saveErr
		}
		if !owned {
			p.log(ctx, c, "lease_lost")
			return ErrLeaseLost
		}
		return err
	}
	owned, err = p.store.Published(dbCtx, c)
	if err != nil {
		p.log(ctx, c, "mark_failed")
		return err
	}
	if !owned {
		p.log(ctx, c, "lease_lost")
		return ErrLeaseLost
	}
	p.log(ctx, c, "published")
	return nil
}
func (p *Publisher) log(ctx context.Context, c ports.OutboxClaim, result string) {
	var metadata struct {
		CorrelationID string `json:"correlationId"`
	}
	_ = json.Unmarshal(c.Event.Payload, &metadata)
	p.logger.InfoContext(ctx, "Outbox publication", "eventId", c.Event.EventID, "eventType", c.Event.EventType, "aggregateId", c.Event.AggregateID, "correlationId", metadata.CorrelationID, "retryCount", c.Event.RetryCount, "publisherId", p.options.PublisherID, "claimToken", c.Token, "outcome", result)
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
