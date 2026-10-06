package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awssqs "github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/aws/smithy-go/middleware"
	"github.com/jackc/pgx/v5/pgxpool"
	sqsadapter "github.com/junglegaming/backend-challenge-go/internal/adapter/sqs"
	"github.com/junglegaming/backend-challenge-go/internal/application/financial"
	"github.com/junglegaming/backend-challenge-go/internal/application/outbox"
	"github.com/junglegaming/backend-challenge-go/internal/domain/wager"
	"github.com/junglegaming/backend-challenge-go/internal/ports"
)

func phase7Pool(t *testing.T) (*pgxpool.Pool, context.Context) {
	t.Helper()
	pool, ctx := phase5Pool(t)
	applyMigration(t, ctx, pool, "000005_outbox_publication_leases.up.sql")
	return pool, ctx
}

type outboxFixture struct {
	ctx    context.Context
	pool   *pgxpool.Pool
	client *awssqs.Client
	queue  string
}

func newOutboxFixture(t *testing.T) *outboxFixture {
	t.Helper()
	if os.Getenv("TEST_SQS_ENDPOINT") == "" {
		t.Skip("set TEST_SQS_ENDPOINT for PostgreSQL + LocalStack Outbox integration")
	}
	pool, _ := phase7Pool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	t.Cleanup(cancel)
	client := realSQSClient(t, ctx)
	queue, err := client.CreateQueue(ctx, &awssqs.CreateQueueInput{QueueName: aws.String(fmt.Sprintf("phase7-%d.fifo", time.Now().UnixNano())), Attributes: map[string]string{"FifoQueue": "true", "ContentBasedDeduplication": "false"}})
	must(t, err)
	f := &outboxFixture{ctx: ctx, pool: pool, client: client, queue: aws.ToString(queue.QueueUrl)}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, err := client.DeleteQueue(ctx, &awssqs.DeleteQueueInput{QueueUrl: aws.String(f.queue)})
		if err != nil {
			t.Error(err)
		}
	})
	return f
}
func (f *outboxFixture) publisher(t *testing.T, pool *pgxpool.Pool, client *awssqs.Client, url, id string) *outbox.Publisher {
	t.Helper()
	p, err := outbox.NewPublisher(NewPublicationStore(pool), sqsadapter.NewEventSender(client, url), outbox.Options{PublisherID: id, PollInterval: 10 * time.Millisecond, BatchSize: 10, BaseRetryDelay: 5 * time.Second, MaxRetryDelay: time.Minute, ClaimDuration: 30 * time.Second, PublishTimeout: 5 * time.Second, StoreTimeout: 3 * time.Second}, slog.New(slog.NewJSONHandler(io.Discard, nil)))
	must(t, err)
	return p
}
func (f *outboxFixture) otherPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	p, err := pgxpool.NewWithConfig(f.ctx, f.pool.Config())
	must(t, err)
	t.Cleanup(p.Close)
	return p
}
func (f *outboxFixture) event(t *testing.T, id, aggregate string) ports.OutboxEvent {
	t.Helper()
	at := time.Now().UTC().Truncate(time.Microsecond)
	body, err := json.Marshal(financial.EventEnvelope{EventID: id, EventType: financial.EventWalletBalanceChanged, AggregateID: aggregate, CorrelationID: "correlation", CausationID: "transaction", OccurredAt: at, Version: 1, Data: map[string]any{"walletId": aggregate, "money": map[string]string{"amount": "25.00", "currency": "BRL"}}})
	must(t, err)
	return ports.OutboxEvent{EventID: id, AggregateID: aggregate, EventType: financial.EventWalletBalanceChanged, Payload: body, OccurredAt: at, NextAttemptAt: at}
}
func (f *outboxFixture) insert(t *testing.T, e ports.OutboxEvent) {
	t.Helper()
	must(t, NewRepositories(f.pool).Outbox.Create(f.ctx, e))
}
func (f *outboxFixture) claim(t *testing.T, owner, token string) ports.OutboxClaim {
	t.Helper()
	claims, err := NewPublicationStore(f.pool).Claim(f.ctx, owner, token, 1, 30*time.Second)
	must(t, err)
	if len(claims) != 1 {
		t.Fatalf("claim count=%d", len(claims))
	}
	return claims[0]
}
func (f *outboxFixture) expire(t *testing.T, id string) {
	t.Helper()
	_, err := f.pool.Exec(f.ctx, `UPDATE outbox_events SET claimed_at=statement_timestamp()-interval '2 minutes',claim_until=statement_timestamp()-interval '1 second' WHERE event_id=$1`, id)
	must(t, err)
}
func (f *outboxFixture) receive(t *testing.T, count int) []types.Message {
	t.Helper()
	ctx, cancel := context.WithTimeout(f.ctx, 20*time.Second)
	defer cancel()
	var messages []types.Message
	for len(messages) < count {
		out, err := f.client.ReceiveMessage(ctx, &awssqs.ReceiveMessageInput{QueueUrl: aws.String(f.queue), MaxNumberOfMessages: 10, WaitTimeSeconds: 1, MessageSystemAttributeNames: []types.MessageSystemAttributeName{types.MessageSystemAttributeNameAll}})
		must(t, err)
		for _, m := range out.Messages {
			messages = append(messages, m)
			_, err := f.client.DeleteMessage(ctx, &awssqs.DeleteMessageInput{QueueUrl: aws.String(f.queue), ReceiptHandle: m.ReceiptHandle})
			must(t, err)
		}
	}
	if len(messages) != count {
		t.Fatal("unexpected event count", len(messages), count)
	}
	return messages
}
func (f *outboxFixture) verifyMessage(t *testing.T, m types.Message, e ports.OutboxEvent) {
	t.Helper()
	if aws.ToString(m.Body) != string(e.Payload) {
		t.Fatal("persisted JSONB envelope changed")
	}
	if m.Attributes["MessageDeduplicationId"] != sqsadapter.EventDeduplicationID(e.EventID) || m.Attributes["MessageGroupId"] != sqsadapter.EventMessageGroupID(e.AggregateID) {
		t.Fatal("FIFO event identity changed", m.Attributes)
	}
}

func TestIntegrationOutboxMigrationUpDownUpPreservesEvents(t *testing.T) {
	pool, ctx := phase5Pool(t)
	s := financial.NewService(NewUnitOfWork(pool))
	w := createFinancialWallet(t, ctx, s, "player", 10000)
	runFinancial(t, ctx, s, financialInput(t, w.WalletID, "player", "bet", "key", wager.TypeBet, 2000))
	var before string
	must(t, pool.QueryRow(ctx, `SELECT jsonb_agg(jsonb_build_object('id',event_id,'payload',payload) ORDER BY event_id)::text FROM outbox_events`).Scan(&before))
	applyMigration(t, ctx, pool, "000005_outbox_publication_leases.up.sql")
	var ordered bool
	must(t, pool.QueryRow(ctx, `SELECT count(DISTINCT publication_sequence)=count(*) AND min(publication_sequence)>0 FROM outbox_events`).Scan(&ordered))
	if !ordered {
		t.Fatal("legacy sequence backfill failed")
	}
	applyMigration(t, ctx, pool, "000005_outbox_publication_leases.down.sql")
	applyMigration(t, ctx, pool, "000005_outbox_publication_leases.up.sql")
	var after string
	must(t, pool.QueryRow(ctx, `SELECT jsonb_agg(jsonb_build_object('id',event_id,'payload',payload) ORDER BY event_id)::text FROM outbox_events`).Scan(&after))
	if before != after {
		t.Fatal("migration changed financial envelopes")
	}
	verifyWallet(t, ctx, pool, w.WalletID, 8000, 2)
	countRows(t, ctx, pool, "wallet_ledger_entries", 2)
}

func TestIntegrationOutboxEligibilityLeaseRecoveryAndFencing(t *testing.T) {
	pool, ctx := phase7Pool(t)
	// A minimal real SQL fixture also runs without requiring SQS.
	f := &outboxFixture{ctx: ctx, pool: pool}
	future := f.event(t, "future-head", "wallet-a")
	future.NextAttemptAt = time.Now().Add(time.Hour)
	f.insert(t, future)
	f.insert(t, f.event(t, "blocked-tail", "wallet-a"))
	f.insert(t, f.event(t, "ready", "wallet-b"))
	store := NewPublicationStore(pool)
	claims, err := store.Claim(ctx, "first", "old-token", 10, 30*time.Second)
	must(t, err)
	if len(claims) != 1 || claims[0].Event.EventID != "ready" {
		t.Fatal("future/head filtering failed", claims)
	}
	old := claims[0]
	other, err := pgxpool.NewWithConfig(ctx, pool.Config())
	must(t, err)
	defer other.Close()
	otherStore := NewPublicationStore(other)
	claims, err = otherStore.Claim(ctx, "second", "new-token", 10, 30*time.Second)
	must(t, err)
	if len(claims) != 0 {
		t.Fatal("active lease stolen")
	}
	f.expire(t, "ready")
	claims, err = otherStore.Claim(ctx, "second", "new-token", 10, 30*time.Second)
	must(t, err)
	if len(claims) != 1 {
		t.Fatal("expired lease not recovered")
	}
	for _, action := range []func() (bool, error){func() (bool, error) { return store.Renew(ctx, old, 30*time.Second) }, func() (bool, error) { return store.Published(ctx, old) }, func() (bool, error) { return store.Retry(ctx, old, time.Second) }, func() (bool, error) { return store.Release(ctx, old) }} {
		ok, err := action()
		must(t, err)
		if ok {
			t.Fatal("stale token mutated recovered claim")
		}
	}
	ok, err := otherStore.Retry(ctx, claims[0], 5*time.Second)
	must(t, err)
	if !ok {
		t.Fatal("retry not recorded")
	}
	e, err := NewRepositories(pool).Outbox.Get(ctx, "ready")
	must(t, err)
	if e.RetryCount != 1 || !e.NextAttemptAt.After(time.Now()) || e.PublishedAt != nil {
		t.Fatal(e)
	}
	claims, err = store.Claim(ctx, "first", "third-token", 10, 30*time.Second)
	must(t, err)
	if len(claims) != 0 {
		t.Fatal("retry date ignored")
	}
	_, err = pool.Exec(ctx, `UPDATE outbox_events SET next_attempt_at=statement_timestamp()-interval '1 second' WHERE event_id='ready'`)
	must(t, err)
	claims, err = store.Claim(ctx, "first", "fourth-token", 10, 30*time.Second)
	must(t, err)
	if len(claims) != 1 || claims[0].Event.RetryCount != 1 {
		t.Fatal(claims)
	}
	// Claim also skips a real row lock rather than blocking an unrelated aggregate.
	_, err = pool.Exec(ctx, `UPDATE outbox_events SET next_attempt_at=statement_timestamp()-interval '1 second' WHERE event_id='future-head'`)
	must(t, err)
	lock, err := pool.Begin(ctx)
	must(t, err)
	defer lock.Rollback(context.Background())
	_, err = lock.Exec(ctx, `SELECT event_id FROM outbox_events WHERE event_id='future-head' FOR UPDATE`)
	must(t, err)
	short, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	claims, err = otherStore.Claim(short, "second", "skip-locked", 10, 30*time.Second)
	must(t, err)
	if len(claims) != 0 {
		t.Fatal("locked head was bypassed by its tail")
	}
}

func TestIntegrationOutboxUncommittedAndRolledBackEventsNeverPublished(t *testing.T) {
	f := newOutboxFixture(t)
	e := f.event(t, "uncommitted", "wallet")
	tx, err := f.pool.Begin(f.ctx)
	must(t, err)
	defer tx.Rollback(context.Background())
	must(t, repositories(tx, true).Outbox.Create(f.ctx, e))
	p := f.publisher(t, f.otherPool(t), f.client, f.queue, "publisher")
	n, err := p.RunOnce(f.ctx, f.ctx)
	must(t, err)
	if n != 0 {
		t.Fatal("published before commit")
	}
	must(t, tx.Rollback(f.ctx))
	n, err = p.RunOnce(f.ctx, f.ctx)
	must(t, err)
	if n != 0 {
		t.Fatal("rollback left publishable event")
	}
	f.insert(t, e)
	n, err = p.RunOnce(f.ctx, f.ctx)
	must(t, err)
	if n != 1 {
		t.Fatal(n)
	}
	stored, err := NewRepositories(f.pool).Outbox.Get(f.ctx, e.EventID)
	must(t, err)
	if stored.PublishedAt == nil {
		t.Fatal("event not marked")
	}
	f.verifyMessage(t, f.receive(t, 1)[0], stored)
}

func TestIntegrationOutboxRealSendFailureAndPersistentRetry(t *testing.T) {
	f := newOutboxFixture(t)
	e := f.event(t, "event", "wallet")
	f.insert(t, e)
	bad := f.publisher(t, f.pool, f.client, f.queue+"-missing", "first")
	n, err := bad.RunOnce(f.ctx, f.ctx)
	if n != 1 || err == nil {
		t.Fatal("real nonexistent queue did not fail", n, err)
	}
	e, err = NewRepositories(f.pool).Outbox.Get(f.ctx, e.EventID)
	must(t, err)
	if e.PublishedAt != nil || e.RetryCount != 1 || !e.NextAttemptAt.After(time.Now()) {
		t.Fatal(e)
	}
	var released bool
	must(t, f.pool.QueryRow(f.ctx, `SELECT claim_token IS NULL FROM outbox_events WHERE event_id=$1`, e.EventID).Scan(&released))
	if !released {
		t.Fatal("retry kept claim")
	}
	restarted := f.publisher(t, f.otherPool(t), realSQSClient(t, f.ctx), f.queue, "restarted")
	n, err = restarted.RunOnce(f.ctx, f.ctx)
	must(t, err)
	if n != 0 {
		t.Fatal("future retry sent early")
	}
	_, err = f.pool.Exec(f.ctx, `UPDATE outbox_events SET next_attempt_at=statement_timestamp()-interval '1 second' WHERE event_id=$1`, e.EventID)
	must(t, err)
	n, err = bad.RunOnce(f.ctx, f.ctx)
	if n != 1 || err == nil {
		t.Fatal("second real send failure not recorded", n, err)
	}
	e, err = NewRepositories(f.pool).Outbox.Get(f.ctx, e.EventID)
	must(t, err)
	if e.RetryCount != 2 || !e.NextAttemptAt.After(time.Now().Add(8*time.Second)) {
		t.Fatal("persistent exponential retry missing", e)
	}
	countRows(t, f.ctx, f.pool, "outbox_events", 1)
	_, err = f.pool.Exec(f.ctx, `UPDATE outbox_events SET next_attempt_at=statement_timestamp()-interval '1 second' WHERE event_id=$1`, e.EventID)
	must(t, err)
	n, err = restarted.RunOnce(f.ctx, f.ctx)
	must(t, err)
	if n != 1 {
		t.Fatal(n)
	}
	e, err = NewRepositories(f.pool).Outbox.Get(f.ctx, e.EventID)
	must(t, err)
	if e.PublishedAt == nil || e.RetryCount != 2 {
		t.Fatal("success reset history", e)
	}
	f.verifyMessage(t, f.receive(t, 1)[0], e)
}

// SDK middleware only observes/gates actual requests; it never substitutes a
// response or broker. Every successful send continues to real LocalStack.
func observedClient(base *awssqs.Client, observe func(context.Context, *awssqs.SendMessageInput) error) *awssqs.Client {
	return awssqs.New(base.Options(), func(o *awssqs.Options) {
		o.APIOptions = append(o.APIOptions, func(stack *middleware.Stack) error {
			return stack.Initialize.Add(middleware.InitializeMiddlewareFunc("observe-real-send", func(ctx context.Context, in middleware.InitializeInput, next middleware.InitializeHandler) (middleware.InitializeOutput, middleware.Metadata, error) {
				if send, ok := in.Parameters.(*awssqs.SendMessageInput); ok {
					if err := observe(ctx, send); err != nil {
						return middleware.InitializeOutput{}, middleware.Metadata{}, err
					}
				}
				return next.HandleInitialize(ctx, in)
			}), middleware.Before)
		})
	})
}
func TestIntegrationOutboxCrashBoundariesAndMarkFailure(t *testing.T) {
	for _, mode := range []string{"before-send", "after-send-before-mark", "mark-failure"} {
		t.Run(mode, func(t *testing.T) {
			f := newOutboxFixture(t)
			e := f.event(t, "event", "wallet")
			f.insert(t, e)
			var mu sync.Mutex
			var attempts []*awssqs.SendMessageInput
			client := observedClient(f.client, func(_ context.Context, in *awssqs.SendMessageInput) error {
				copy := *in
				mu.Lock()
				attempts = append(attempts, &copy)
				mu.Unlock()
				return nil
			})
			claim := f.claim(t, "crashed", "first-token")
			if mode == "after-send-before-mark" {
				must(t, sqsadapter.NewEventSender(client, f.queue).Send(f.ctx, claim.Event))
			}
			if mode == "mark-failure" {
				_, err := f.pool.Exec(f.ctx, `CREATE FUNCTION fail_outbox_mark() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.published_at IS NOT NULL THEN RAISE EXCEPTION 'injected mark failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER outbox_mark_failure BEFORE UPDATE ON outbox_events FOR EACH ROW EXECUTE FUNCTION fail_outbox_mark();`)
				must(t, err)
				if err := f.publisher(t, f.pool, client, f.queue, "crashed").PublishClaim(f.ctx, claim); err == nil {
					t.Fatal("mark failure hidden")
				}
				_, err = f.pool.Exec(f.ctx, `DROP TRIGGER outbox_mark_failure ON outbox_events`)
				must(t, err)
			}
			pending, err := NewRepositories(f.pool).Outbox.Get(f.ctx, e.EventID)
			must(t, err)
			if pending.PublishedAt != nil {
				t.Fatal("crash marked success")
			}
			f.expire(t, e.EventID)
			p := f.publisher(t, f.otherPool(t), client, f.queue, "restarted")
			n, err := p.RunOnce(f.ctx, f.ctx)
			must(t, err)
			if n != 1 {
				t.Fatal("event lost", n)
			}
			stored, err := NewRepositories(f.pool).Outbox.Get(f.ctx, e.EventID)
			must(t, err)
			if stored.PublishedAt == nil {
				t.Fatal("restart did not mark")
			}
			want := 2
			if mode == "before-send" {
				want = 1
			}
			if len(attempts) != want {
				t.Fatal("wrong real publish attempt count", len(attempts), want)
			}
			for _, in := range attempts {
				if aws.ToString(in.MessageBody) != string(stored.Payload) || aws.ToString(in.MessageDeduplicationId) != sqsadapter.EventDeduplicationID(e.EventID) {
					t.Fatal("crash retry changed stable envelope/identity")
				}
			}
			// Two actual sends within FIFO's dedup window yield one delivery here.
			// This does not assert exactly-once publication beyond that window.
			f.verifyMessage(t, f.receive(t, 1)[0], stored)
		})
	}
}

func TestIntegrationOutboxAllFinancialEventTypes(t *testing.T) {
	f := newOutboxFixture(t)
	s := financial.NewService(NewUnitOfWork(f.pool))
	w := createFinancialWallet(t, f.ctx, s, "player", 10000)
	runFinancial(t, f.ctx, s, financialInput(t, w.WalletID, "player", "bet", "bet-key", wager.TypeBet, 2000))
	runFinancial(t, f.ctx, s, financialInput(t, w.WalletID, "player", "reject", "reject-key", wager.TypeBet, 20000))
	runFinancial(t, f.ctx, s, referenceInput(t, w.WalletID, "player", "pending", "pending-key", wager.TypeRefund, 2000, "future"))
	expected := readOutboxEvents(t, f.ctx, f.pool)
	p := f.publisher(t, f.pool, f.client, f.queue, "publisher")
	for i := 0; i < len(expected); i++ {
		n, err := p.RunOnce(f.ctx, f.ctx)
		must(t, err)
		if n == 0 {
			break
		}
	}
	seen := map[string]bool{}
	for _, m := range f.receive(t, len(expected)) {
		var envelope financial.EventEnvelope
		must(t, json.Unmarshal([]byte(aws.ToString(m.Body)), &envelope))
		e, ok := expected[envelope.EventID]
		if !ok {
			t.Fatal("unknown event")
		}
		f.verifyMessage(t, m, e)
		seen[e.EventType] = true
	}
	for _, kind := range []string{financial.EventWagerProcessed, financial.EventWagerRejected, financial.EventWalletBalanceChanged, financial.EventWagerPendingReference} {
		if !seen[kind] {
			t.Fatal("event type omitted", kind)
		}
	}
	var pending int
	must(t, f.pool.QueryRow(f.ctx, `SELECT count(*) FROM outbox_events WHERE published_at IS NULL`).Scan(&pending))
	if pending != 0 {
		t.Fatal("event not marked", pending)
	}
	verifyWallet(t, f.ctx, f.pool, w.WalletID, 8000, 2)
}
func readOutboxEvents(t *testing.T, ctx context.Context, pool *pgxpool.Pool) map[string]ports.OutboxEvent {
	t.Helper()
	rows, err := pool.Query(ctx, `SELECT event_id,aggregate_id,event_type,payload,occurred_at,retry_count,next_attempt_at,published_at FROM outbox_events`)
	must(t, err)
	defer rows.Close()
	events := map[string]ports.OutboxEvent{}
	for rows.Next() {
		var e ports.OutboxEvent
		must(t, rows.Scan(&e.EventID, &e.AggregateID, &e.EventType, &e.Payload, &e.OccurredAt, &e.RetryCount, &e.NextAttemptAt, &e.PublishedAt))
		events[e.EventID] = e
	}
	must(t, rows.Err())
	return events
}

func TestIntegrationOutbox100EventsThreePublishersAndOrdering(t *testing.T) {
	f := newOutboxFixture(t)
	s := financial.NewService(NewUnitOfWork(f.pool))
	for i := 0; i < 25; i++ {
		player := fmt.Sprintf("player-%d", i)
		w := createFinancialWallet(t, f.ctx, s, player, 10000)
		runFinancial(t, f.ctx, s, financialInput(t, w.WalletID, player, fmt.Sprintf("bet-%d", i), fmt.Sprintf("key-%d", i), wager.TypeBet, 2000))
	}
	expected := readOutboxEvents(t, f.ctx, f.pool)
	if len(expected) != 100 {
		t.Fatal(len(expected))
	}
	publishers := []*outbox.Publisher{f.publisher(t, f.pool, f.client, f.queue, "one"), f.publisher(t, f.otherPool(t), realSQSClient(t, f.ctx), f.queue, "two"), f.publisher(t, f.otherPool(t), realSQSClient(t, f.ctx), f.queue, "three")}
	type batchResult struct {
		index, count int
		err          error
	}
	participation := make([]int, 3)
	for batch := 0; batch < 20; batch++ {
		errs := make(chan batchResult, 3)
		var ready sync.WaitGroup
		ready.Add(3)
		start := make(chan struct{})
		for index, p := range publishers {
			go func(index int, p *outbox.Publisher) {
				ready.Done()
				<-start
				count, err := p.RunOnce(f.ctx, f.ctx)
				errs <- batchResult{index, count, err}
			}(index, p)
		}
		ready.Wait()
		close(start)
		for i := 0; i < 3; i++ {
			result := <-errs
			must(t, result.err)
			participation[result.index] += result.count
		}
		var pending int
		must(t, f.pool.QueryRow(f.ctx, `SELECT count(*) FROM outbox_events WHERE published_at IS NULL`).Scan(&pending))
		if pending == 0 {
			break
		}
		if batch == 19 {
			t.Fatal("publishers lost pending events")
		}
	}
	for index, count := range participation {
		if count == 0 {
			t.Fatal("publisher never participated", index)
		}
	}
	sequence := map[string]int64{}
	rows, err := f.pool.Query(f.ctx, `SELECT event_id,publication_sequence FROM outbox_events ORDER BY publication_sequence`)
	must(t, err)
	for rows.Next() {
		var id string
		var n int64
		must(t, rows.Scan(&id, &n))
		sequence[id] = n
	}
	must(t, rows.Err())
	rows.Close()
	seen := map[string]bool{}
	last := map[string]int64{}
	for _, m := range f.receive(t, 100) {
		var envelope financial.EventEnvelope
		must(t, json.Unmarshal([]byte(aws.ToString(m.Body)), &envelope))
		e, ok := expected[envelope.EventID]
		if !ok || seen[e.EventID] {
			t.Fatal("unknown/duplicate event", envelope.EventID)
		}
		f.verifyMessage(t, m, e)
		if sequence[e.EventID] <= last[e.AggregateID] {
			t.Fatal("aggregate reordered")
		}
		last[e.AggregateID] = sequence[e.EventID]
		seen[e.EventID] = true
	}
	if len(seen) != 100 {
		t.Fatal("event lost")
	}
	var marked int
	must(t, f.pool.QueryRow(f.ctx, `SELECT count(*) FROM outbox_events WHERE published_at IS NOT NULL AND claim_token IS NULL`).Scan(&marked))
	if marked != 100 {
		t.Fatal(marked)
	}
}

func TestIntegrationOutboxNetworkDoesNotHoldSQLLocksAndShutdownDrains(t *testing.T) {
	f := newOutboxFixture(t)
	first := f.event(t, "first", "wallet-a")
	f.insert(t, first)
	f.insert(t, f.event(t, "tail", "wallet-a"))
	started, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	client := observedClient(f.client, func(ctx context.Context, _ *awssqs.SendMessageInput) error {
		close(started)
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	})
	p := f.publisher(t, f.pool, client, f.queue, "blocked")
	poll, stop := context.WithCancel(f.ctx)
	defer stop()
	go func() { defer close(done); p.Run(poll, f.ctx) }()
	select {
	case <-started:
	case <-f.ctx.Done():
		t.Fatal("publisher never sent")
	}
	otherPool := f.otherPool(t)
	// Locking this claimed row succeeds NOWAIT: no publisher SQL tx is held over I/O.
	tx, err := otherPool.Begin(f.ctx)
	must(t, err)
	defer tx.Rollback(context.Background())
	_, err = tx.Exec(f.ctx, `SELECT event_id FROM outbox_events WHERE event_id='first' FOR UPDATE NOWAIT`)
	must(t, err)
	must(t, tx.Commit(f.ctx))
	stored, err := NewRepositories(f.pool).Outbox.Get(f.ctx, "first")
	must(t, err)
	if stored.PublishedAt != nil {
		t.Fatal("marked before real broker ACK")
	}
	f.insert(t, f.event(t, "independent", "wallet-b"))
	other := f.publisher(t, otherPool, realSQSClient(t, f.ctx), f.queue, "independent")
	n, err := other.RunOnce(f.ctx, f.ctx)
	must(t, err)
	if n != 1 {
		t.Fatal("independent aggregate did not progress", n)
	}
	stop()
	close(release)
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("publisher did not drain")
	}
	stored, err = NewRepositories(f.pool).Outbox.Get(f.ctx, "first")
	must(t, err)
	if stored.PublishedAt == nil {
		t.Fatal("active event did not finish")
	}
	tail, err := NewRepositories(f.pool).Outbox.Get(f.ctx, "tail")
	must(t, err)
	if tail.PublishedAt != nil {
		t.Fatal("shutdown started new event")
	}
	n, err = other.RunOnce(f.ctx, f.ctx)
	must(t, err)
	if n != 1 {
		t.Fatal("tail not recovered", n)
	}
	f.receive(t, 3)
}
