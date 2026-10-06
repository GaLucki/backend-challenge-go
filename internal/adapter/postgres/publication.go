package postgres

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/junglegaming/backend-challenge-go/internal/ports"
)

type publicationStore struct{ pool *pgxpool.Pool }

func NewPublicationStore(pool *pgxpool.Pool) ports.PublicationStore {
	return &publicationStore{pool: pool}
}

// One autocommit statement claims at most one outstanding head per aggregate.
// An earlier not-due/leased event blocks its tail but not other aggregates.
func (s *publicationStore) Claim(ctx context.Context, owner, token string, limit int, lease time.Duration) ([]ports.OutboxClaim, error) {
	if strings.TrimSpace(owner) == "" || strings.TrimSpace(token) == "" || limit < 1 || lease < time.Microsecond {
		return nil, fmt.Errorf("invalid Outbox claim")
	}
	rows, err := s.pool.Query(ctx, `WITH heads AS (
 SELECT e.event_id FROM outbox_events e
 WHERE e.published_at IS NULL AND e.next_attempt_at<=statement_timestamp()
   AND (e.claim_until IS NULL OR e.claim_until<=statement_timestamp())
   AND NOT EXISTS(SELECT 1 FROM outbox_events earlier
       WHERE earlier.aggregate_id=e.aggregate_id AND earlier.published_at IS NULL
         AND earlier.publication_sequence<e.publication_sequence)
 ORDER BY e.publication_sequence LIMIT $1 FOR UPDATE OF e SKIP LOCKED
)
UPDATE outbox_events e SET claimed_at=statement_timestamp(),
 claim_until=statement_timestamp()+$4*interval '1 microsecond',claimed_by=$2,claim_token=$3
FROM heads WHERE e.event_id=heads.event_id
RETURNING e.event_id,e.aggregate_id,e.event_type,e.payload,e.occurred_at,e.retry_count,
 e.next_attempt_at,e.published_at,e.publication_sequence,e.claimed_by,e.claim_token,e.claim_until`, limit, owner, token, lease.Microseconds())
	if err != nil {
		return nil, mapError(err)
	}
	defer rows.Close()
	var claims []ports.OutboxClaim
	for rows.Next() {
		var c ports.OutboxClaim
		if err := rows.Scan(&c.Event.EventID, &c.Event.AggregateID, &c.Event.EventType, &c.Event.Payload, &c.Event.OccurredAt, &c.Event.RetryCount, &c.Event.NextAttemptAt, &c.Event.PublishedAt, &c.Sequence, &c.ClaimedBy, &c.Token, &c.ClaimUntil); err != nil {
			return nil, mapError(err)
		}
		claims = append(claims, c)
	}
	if err := rows.Err(); err != nil {
		return nil, mapError(err)
	}
	// Closing/exhausting rows completes the implicit transaction before returning.
	return claims, nil
}
func (s *publicationStore) Renew(ctx context.Context, c ports.OutboxClaim, lease time.Duration) (bool, error) {
	if lease < time.Microsecond {
		return false, fmt.Errorf("invalid Outbox lease")
	}
	tag, err := s.pool.Exec(ctx, `UPDATE outbox_events SET claim_until=statement_timestamp()+$3*interval '1 microsecond'
 WHERE event_id=$1 AND claim_token=$2 AND published_at IS NULL AND claim_until>statement_timestamp()`, c.Event.EventID, c.Token, lease.Microseconds())
	return tag.RowsAffected() == 1, mapError(err)
}
func (s *publicationStore) Published(ctx context.Context, c ports.OutboxClaim) (bool, error) {
	tag, err := s.pool.Exec(ctx, `UPDATE outbox_events SET published_at=statement_timestamp(),
 claimed_at=NULL,claim_until=NULL,claimed_by=NULL,claim_token=NULL
 WHERE event_id=$1 AND claim_token=$2 AND published_at IS NULL AND claim_until>statement_timestamp()`, c.Event.EventID, c.Token)
	return tag.RowsAffected() == 1, mapError(err)
}
func (s *publicationStore) Retry(ctx context.Context, c ports.OutboxClaim, delay time.Duration) (bool, error) {
	if delay < time.Microsecond {
		return false, fmt.Errorf("invalid Outbox retry delay")
	}
	tag, err := s.pool.Exec(ctx, `UPDATE outbox_events SET retry_count=LEAST(retry_count::bigint+1,2147483647)::integer,
 next_attempt_at=statement_timestamp()+$3*interval '1 microsecond',
 claimed_at=NULL,claim_until=NULL,claimed_by=NULL,claim_token=NULL
 WHERE event_id=$1 AND claim_token=$2 AND published_at IS NULL AND claim_until>statement_timestamp()`, c.Event.EventID, c.Token, delay.Microseconds())
	return tag.RowsAffected() == 1, mapError(err)
}
func (s *publicationStore) Release(ctx context.Context, c ports.OutboxClaim) (bool, error) {
	tag, err := s.pool.Exec(ctx, `UPDATE outbox_events SET claimed_at=NULL,claim_until=NULL,claimed_by=NULL,claim_token=NULL
 WHERE event_id=$1 AND claim_token=$2 AND published_at IS NULL`, c.Event.EventID, c.Token)
	return tag.RowsAffected() == 1, mapError(err)
}
