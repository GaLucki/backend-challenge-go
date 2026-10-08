package application_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awssqs "github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/junglegaming/backend-challenge-go/internal/adapter/postgres"
	sqsadapter "github.com/junglegaming/backend-challenge-go/internal/adapter/sqs"
	"github.com/junglegaming/backend-challenge-go/internal/application/outbox"
	"github.com/junglegaming/backend-challenge-go/internal/config"
	"github.com/junglegaming/backend-challenge-go/internal/domain/money"
	"github.com/junglegaming/backend-challenge-go/internal/domain/wager"
	"github.com/junglegaming/backend-challenge-go/internal/observability"
)

func observedMetric(t *testing.T, m *observability.Metrics, name string, labels map[string]string) float64 {
	t.Helper()
	fs, err := m.Registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range fs {
		if f.GetName() != name {
			continue
		}
		for _, metric := range f.Metric {
			matches := len(metric.Label) == len(labels)
			for _, l := range metric.Label {
				if labels[l.GetName()] != l.GetValue() {
					matches = false
				}
			}
			if !matches {
				continue
			}
			if metric.Counter != nil {
				return metric.GetCounter().GetValue()
			}
			if metric.Gauge != nil {
				return metric.GetGauge().GetValue()
			}
			if metric.Histogram != nil {
				return float64(metric.GetHistogram().GetSampleCount())
			}
		}
	}
	return 0
}
func requireObserved(t *testing.T, m *observability.Metrics, name string, labels map[string]string, want float64) {
	t.Helper()
	if got := observedMetric(t, m, "wagering_"+name, labels); got != want {
		t.Fatalf("%s %v = %v want %v", name, labels, got, want)
	}
}

func TestIntegrationObservabilityHTTPFinancialAndReadiness(t *testing.T) {
	t.Setenv("METRICS_ENABLED", "true")
	f := newAPIFixture(t)
	for _, tc := range []struct {
		identity string
		status   int
	}{{"", 401}, {"provider-a", 403}, {"provider-b", 403}, {"internal-service", 200}} {
		f.request(t, "GET", "/metrics", tc.identity, nil, "", tc.status)
	}
	w := f.wallet(t, "phase11-player", "100.00")
	wid := string(w.WalletID)
	body := wagerPayload(wid, "phase11-player", "bet", "BET", "20.00", "")
	f.wager(t, "provider-a", body, "bet", 201)
	requireObserved(t, f.metrics, "financial_operations_total", map[string]string{"operation": "BET", "state": "PROCESSED"}, 1)
	f.wager(t, "provider-a", body, "bet", 200)
	requireObserved(t, f.metrics, "idempotency_events_total", map[string]string{"outcome": "replay"}, 1)
	requireObserved(t, f.metrics, "financial_movements_total", map[string]string{"operation": "BET"}, 1)
	f.wager(t, "provider-a", wagerPayload(wid, "phase11-player", "rejected", "BET", "200.00", ""), "rejected", 422)
	requireObserved(t, f.metrics, "financial_operations_total", map[string]string{"operation": "BET", "state": "REJECTED"}, 1)
	f.request(t, "POST", "/wagering/transactions", "provider-a", wagerPayload(wid, "phase11-player", "bet", "BET", "21.00", ""), "bet", 409)
	f.request(t, "POST", "/wagering/transactions", "provider-a", body, "external-conflict", 409)
	requireObserved(t, f.metrics, "idempotency_events_total", map[string]string{"outcome": "idempotency_conflict"}, 1)
	requireObserved(t, f.metrics, "idempotency_events_total", map[string]string{"outcome": "external_conflict"}, 1)
	f.wager(t, "provider-a", wagerPayload(wid, "phase11-player", "pending", "REFUND", "5.00", "future"), "pending", 202)
	requireObserved(t, f.metrics, "financial_operations_total", map[string]string{"operation": "REFUND", "state": "PENDING_REFERENCE"}, 1)
	if err := f.collector.CollectOnce(f.ctx); err != nil {
		t.Fatal(err)
	}
	requireObserved(t, f.metrics, "pending_references", nil, 1)
	if observedMetric(t, f.metrics, "wagering_outbox_pending", nil) < 1 {
		t.Fatal("missing outbox backlog gauge")
	}
	if observedMetric(t, f.metrics, "wagering_postgres_pool_connections", nil) < 1 {
		t.Fatal("missing pgxpool stats")
	}
	// A real retry must not add a transaction or financial movement.
	if handled, err := f.core.ResolvePendingOnce(f.ctx, time.Now().UTC().Add(2*time.Second)); err != nil || !handled {
		t.Fatal("pending retry", handled, err)
	}
	requireObserved(t, f.metrics, "pending_events_total", map[string]string{"outcome": "retry"}, 1)
	requireObserved(t, f.metrics, "financial_movements_total", map[string]string{"operation": "REFUND"}, 0)
	f.wager(t, "provider-a", wagerPayload(wid, "phase11-player", "future", "BET", "5.00", ""), "future", 201)
	if handled, err := f.core.ResolvePendingOnce(f.ctx, time.Now().UTC().Add(10*time.Second)); err != nil || !handled {
		t.Fatal("pending resolution", handled, err)
	}
	requireObserved(t, f.metrics, "pending_events_total", map[string]string{"outcome": "resolved"}, 1)
	requireObserved(t, f.metrics, "financial_movements_total", map[string]string{"operation": "REFUND"}, 1)
	requireObserved(t, f.metrics, "financial_operations_total", map[string]string{"operation": "REFUND", "state": "PROCESSED"}, 0)
	f.wager(t, "provider-a", wagerPayload(wid, "phase11-player", "expired", "REFUND", "1.00", "absent"), "expired", 202)
	if handled, err := f.core.ResolvePendingOnce(f.ctx, time.Now().UTC().Add(time.Hour)); err != nil || !handled {
		t.Fatal("pending expiration", handled, err)
	}
	requireObserved(t, f.metrics, "pending_events_total", map[string]string{"outcome": "expired"}, 1)
	f.request(t, "POST", "/wallets/"+wid+"/reconciliation", "internal-service", nil, "", 200)
	requireObserved(t, f.metrics, "reconciliation_runs_total", map[string]string{"result": "CONSISTENT"}, 1)
	_, err := f.pool.Exec(f.ctx, `UPDATE wallets SET balance_cents=7900 WHERE id=$1`, wid)
	if err != nil {
		t.Fatal(err)
	}
	f.request(t, "POST", "/wallets/"+wid+"/reconciliation", "internal-service", nil, "", 200)
	requireObserved(t, f.metrics, "reconciliation_runs_total", map[string]string{"result": "DIVERGENT"}, 1)
	if observedMetric(t, f.metrics, "wagering_reconciliation_divergences_total", map[string]string{"code": "BALANCE_MISMATCH"}) < 1 {
		t.Fatal("divergence counter missing")
	}
	f.request(t, "POST", "/wallets/missing/reconciliation", "internal-service", nil, "", 404)
	requireObserved(t, f.metrics, "reconciliation_runs_total", map[string]string{"result": "ERROR"}, 1)
	f.request(t, "GET", "/wallets/"+wid, "internal-service", nil, "", 200)
	requireObserved(t, f.metrics, "http_requests_total", map[string]string{"method": "GET", "route": "/wallets/{walletId}", "status": "200"}, 1)
	f.request(t, "GET", "/health/ready", "", nil, "", 200)
	requireObserved(t, f.metrics, "ready", nil, 1)
	raw := f.request(t, "GET", "/metrics", "internal-service", nil, "", 200)
	for _, private := range []string{wid, "phase11-player", "provider-a", "transactionId=", "walletId=", "messageId="} {
		if strings.Contains(string(raw), private) {
			t.Fatal("private metric label", private)
		}
	}
	fs, err := f.metrics.Registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, family := range fs {
		for _, metric := range family.Metric {
			for _, label := range metric.Label {
				switch label.GetName() {
				case "walletId", "playerId", "transactionId", "providerId", "messageId", "eventId":
					t.Fatal("high cardinality label", label)
				}
			}
		}
	}
	// Closing the actual pgx pool exercises real dependency failure, with the
	// listener still running. Liveness remains up; readiness and audit fail.
	f.pool.Close()
	f.request(t, "GET", "/health/live", "", nil, "", 200)
	f.request(t, "GET", "/health/ready", "", nil, "", 503)
	requireObserved(t, f.metrics, "ready", nil, 0)
	f.request(t, "POST", "/wallets/"+wid+"/reconciliation", "internal-service", nil, "", 503)
	requireObserved(t, f.metrics, "dependency_failures_total", map[string]string{"dependency": "postgres", "component": "readiness"}, 1)
	f.stop(t)
	if _, err = f.client.Get(f.base + "/health/live"); err == nil {
		t.Fatal("HTTP listener survived shutdown")
	}
}

type observationQueues struct {
	client                                    *awssqs.Client
	commands, manual, events, dlqURL, dlqName string
}

func newObservationQueues(t *testing.T, ctx context.Context) *observationQueues {
	t.Helper()
	endpoint := os.Getenv("TEST_SQS_ENDPOINT")
	if endpoint == "" {
		t.Skip("set TEST_SQS_ENDPOINT for real operational metrics")
	}
	client, err := sqsadapter.NewClient(ctx, config.Config{AWSRegion: "us-east-1", SQSEndpoint: endpoint, AWSAccessKeyID: "test", AWSSecretAccessKey: "test"})
	if err != nil {
		t.Fatal(err)
	}
	q := &observationQueues{client: client}
	prefix := fmt.Sprintf("phase11-%d", time.Now().UnixNano())
	create := func(suffix string, redrive string) string {
		attrs := map[string]string{"FifoQueue": "true", "ContentBasedDeduplication": "false"}
		if redrive != "" {
			attrs["RedrivePolicy"] = redrive
		}
		out, err := client.CreateQueue(ctx, &awssqs.CreateQueueInput{QueueName: aws.String(prefix + suffix + ".fifo"), Attributes: attrs})
		if err != nil {
			t.Fatal(err)
		}
		url := aws.ToString(out.QueueUrl)
		t.Cleanup(func() {
			cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if _, err := client.DeleteQueue(cleanup, &awssqs.DeleteQueueInput{QueueUrl: aws.String(url)}); err != nil {
				t.Error(err)
			}
		})
		return url
	}
	q.dlqName = prefix + "-dlq.fifo"
	q.dlqURL = create("-dlq", "")
	attrs, err := client.GetQueueAttributes(ctx, &awssqs.GetQueueAttributesInput{QueueUrl: aws.String(q.dlqURL), AttributeNames: []types.QueueAttributeName{types.QueueAttributeNameQueueArn}})
	if err != nil {
		t.Fatal(err)
	}
	policy, _ := json.Marshal(map[string]any{"deadLetterTargetArn": attrs.Attributes["QueueArn"], "maxReceiveCount": 2})
	q.commands = create("-commands", string(policy))
	q.manual = create("-manual", string(policy))
	q.events = create("-events", "")
	return q
}

func TestIntegrationObservabilitySQSOutboxDLQAndLifecycle(t *testing.T) {
	t.Setenv("METRICS_ENABLED", "true")
	f := newAPIFixture(t)
	queues := newObservationQueues(t, f.ctx)
	f.stop(t)
	t.Setenv("SQS_ENABLED", "true")
	t.Setenv("SQS_QUEUE_URL", queues.commands)
	t.Setenv("SQS_DLQ_NAME", queues.dlqName)
	t.Setenv("AWS_REGION", "us-east-1")
	t.Setenv("AWS_ACCESS_KEY_ID", "test")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "test")
	t.Setenv("SQS_ENDPOINT", os.Getenv("TEST_SQS_ENDPOINT"))
	t.Setenv("METRICS_COLLECT_INTERVAL", "2s")
	t.Setenv("METRICS_COLLECT_TIMEOUT", "1s")
	f.start(t)
	// Stop workers before deleting their disposable queues (cleanup is LIFO).
	t.Cleanup(func() { f.stop(t) })
	w := f.wallet(t, "phase11-sqs-player", "100.00")
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	options := sqsadapter.Options{QueueURL: queues.manual, ConsumerName: "phase11-manual", WaitSeconds: 1, VisibilitySeconds: 30, MaxMessages: 1, Concurrency: 1, ProcessingTimeout: 5 * time.Second, AckTimeout: 3 * time.Second, ReceiveRetryDelay: time.Second}
	consumer, err := sqsadapter.NewConsumer(queues.client, f.core, options, logger)
	if err != nil {
		t.Fatal(err)
	}
	consumer.WithTelemetry(f.metrics)
	envelope := sqsadapter.Envelope{SchemaVersion: 1, MessageID: "metrics-delivery", ProviderID: "provider-a", ExternalTransactionID: "metrics-bet", PlayerID: "phase11-sqs-player", WalletID: string(w.WalletID), Type: wager.TypeBet, Money: money.External{Amount: "20.00", Currency: "BRL"}, RoundID: "round", CorrelationID: "phase11-correlation", CausationID: "phase11-cause", OccurredAt: time.Now().UTC()}
	deliver := func(e sqsadapter.Envelope, dedup string) types.Message {
		t.Helper()
		in, err := sqsadapter.BuildSendInput(queues.manual, e)
		if err != nil {
			t.Fatal(err)
		}
		in.MessageDeduplicationId = aws.String(dedup)
		if _, err := queues.client.SendMessage(f.ctx, in); err != nil {
			t.Fatal(err)
		}
		messages, err := consumer.Receive(f.ctx)
		if err != nil || len(messages) != 1 {
			t.Fatal("receive", err, len(messages))
		}
		return messages[0]
	}
	first := deliver(envelope, "first")
	if err := consumer.Handle(f.ctx, first); err != nil {
		t.Fatal(err)
	}
	second := deliver(envelope, "duplicate")
	if err := consumer.Handle(f.ctx, second); err != nil {
		t.Fatal(err)
	}
	requireObserved(t, f.metrics, "sqs_events_total", map[string]string{"outcome": "received"}, 2)
	requireObserved(t, f.metrics, "sqs_events_total", map[string]string{"outcome": "completed"}, 2)
	requireObserved(t, f.metrics, "sqs_events_total", map[string]string{"outcome": "duplicate"}, 1)
	requireObserved(t, f.metrics, "idempotency_events_total", map[string]string{"outcome": "duplicate"}, 1)
	requireObserved(t, f.metrics, "financial_movements_total", map[string]string{"operation": "BET"}, 1)
	conflict := envelope
	conflict.Money.Amount = "21.00"
	bad := deliver(conflict, "inbox-conflict")
	if err := consumer.Handle(f.ctx, bad); err == nil {
		t.Fatal("Inbox hash conflict accepted")
	}
	requireObserved(t, f.metrics, "idempotency_events_total", map[string]string{"outcome": "inbox_hash_conflict"}, 1)
	requireObserved(t, f.metrics, "sqs_events_total", map[string]string{"outcome": "permanent_failure"}, 1)
	// Same external operation in a new logical delivery must reuse the effect.
	_, err = queues.client.DeleteMessage(f.ctx, &awssqs.DeleteMessageInput{QueueUrl: aws.String(queues.manual), ReceiptHandle: bad.ReceiptHandle})
	if err != nil {
		t.Fatal(err)
	}
	envelope.MessageID = "new-delivery-same-external"
	externalDuplicate := deliver(envelope, "external-duplicate")
	if err := consumer.Handle(f.ctx, externalDuplicate); err != nil {
		t.Fatal(err)
	}
	requireObserved(t, f.metrics, "idempotency_events_total", map[string]string{"outcome": "external_duplicate"}, 1)
	badOptions := options
	badOptions.QueueURL = strings.Replace(queues.manual, "-manual.fifo", "-unavailable.fifo", 1)
	badConsumer, err := sqsadapter.NewConsumer(queues.client, f.core, badOptions, logger)
	if err != nil {
		t.Fatal(err)
	}
	badConsumer.WithTelemetry(f.metrics)
	if err := badConsumer.Handle(f.ctx, externalDuplicate); err == nil {
		t.Fatal("DeleteMessage against absent queue succeeded")
	}
	requireObserved(t, f.metrics, "sqs_events_total", map[string]string{"outcome": "delete_failure"}, 1)
	if _, err := badConsumer.Receive(f.ctx); err == nil {
		t.Fatal("poll against absent queue succeeded")
	}
	if observedMetric(t, f.metrics, "wagering_sqs_events_total", map[string]string{"outcome": "poll_failure"}) < 1 {
		t.Fatal("poll metric missing")
	}
	// Failure goes through the real SDK, with a real claim and persisted retry.
	store := postgres.NewPublicationStore(f.pool)
	pubOptions := outbox.Options{PublisherID: "phase11-publisher", PollInterval: time.Second, BatchSize: 1, BaseRetryDelay: 5 * time.Second, MaxRetryDelay: time.Minute, ClaimDuration: 30 * time.Second, PublishTimeout: 3 * time.Second, StoreTimeout: 2 * time.Second}
	badPublisher, err := outbox.NewPublisher(store, sqsadapter.NewEventSender(queues.client, badOptions.QueueURL), pubOptions, logger)
	if err != nil {
		t.Fatal(err)
	}
	badPublisher.WithTelemetry(f.metrics)
	if _, err := badPublisher.RunOnce(f.ctx, f.ctx); err == nil {
		t.Fatal("publish failure missing")
	}
	requireObserved(t, f.metrics, "outbox_events_total", map[string]string{"outcome": "publish_failure"}, 1)
	requireObserved(t, f.metrics, "outbox_events_total", map[string]string{"outcome": "retry_scheduled"}, 1)
	if err := f.collector.CollectOnce(f.ctx); err != nil {
		t.Fatal(err)
	}
	requireObserved(t, f.metrics, "outbox_retry_scheduled", nil, 1)
	_, err = f.pool.Exec(f.ctx, `UPDATE outbox_events SET next_attempt_at=now() WHERE published_at IS NULL`)
	if err != nil {
		t.Fatal(err)
	}
	publisher, err := outbox.NewPublisher(store, sqsadapter.NewEventSender(queues.client, queues.events), pubOptions, logger)
	if err != nil {
		t.Fatal(err)
	}
	publisher.WithTelemetry(f.metrics)
	for i := 0; i < 10; i++ {
		n, err := publisher.RunOnce(f.ctx, f.ctx)
		if err != nil {
			t.Fatal(err)
		}
		if n == 0 {
			break
		}
	}
	requireObserved(t, f.metrics, "outbox_events_total", map[string]string{"outcome": "published"}, 4)
	if err := f.collector.CollectOnce(f.ctx); err != nil {
		t.Fatal(err)
	}
	requireObserved(t, f.metrics, "outbox_pending", nil, 0)
	requireObserved(t, f.metrics, "outbox_lag_seconds", nil, 0)
	// Depth is sampled from SQS, not inferred from consumer errors/redrive.
	_, err = queues.client.SendMessage(f.ctx, &awssqs.SendMessageInput{QueueUrl: aws.String(queues.dlqURL), MessageBody: aws.String("operational-depth-fixture"), MessageGroupId: aws.String("depth"), MessageDeduplicationId: aws.String("depth")})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.collector.CollectOnce(f.ctx); err != nil {
		t.Fatal(err)
	}
	requireObserved(t, f.metrics, "dlq_collection_enabled", nil, 1)
	requireObserved(t, f.metrics, "dlq_collection_success", nil, 1)
	requireObserved(t, f.metrics, "dlq_depth_approximate", nil, 1)
	var correlation string
	if err := f.pool.QueryRow(f.ctx, `SELECT payload->>'correlationId' FROM outbox_events WHERE payload->>'causationId'=(SELECT transaction_id FROM wager_transactions WHERE external_transaction_id='metrics-bet') LIMIT 1`).Scan(&correlation); err != nil || correlation != "phase11-correlation" {
		t.Fatal("SQS→Inbox→Outbox correlation lost", err, correlation)
	}
	oldMetrics := f.metrics
	oldPool := f.pool
	f.stop(t)
	if oldPool.Stat().TotalConns() != 0 {
		t.Fatal("pool remained open after worker/collector stop")
	}
	requireObserved(t, oldMetrics, "sqs_in_flight", nil, 0)
	requireObserved(t, oldMetrics, "http_in_flight", nil, 0)
	if _, err := f.client.Get(f.base + "/health/live"); err == nil {
		t.Fatal("listener survived shutdown")
	}
}
