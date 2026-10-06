package postgres

import (
	"context"
	"encoding/json"
	"errors"
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
	"github.com/jackc/pgx/v5/pgxpool"
	sqsadapter "github.com/junglegaming/backend-challenge-go/internal/adapter/sqs"
	"github.com/junglegaming/backend-challenge-go/internal/application/financial"
	"github.com/junglegaming/backend-challenge-go/internal/config"
	"github.com/junglegaming/backend-challenge-go/internal/domain/wager"
	"github.com/junglegaming/backend-challenge-go/internal/domain/wallet"
	"github.com/junglegaming/backend-challenge-go/internal/ports"
)

// All transport calls below use the actual AWS SDK against LocalStack. Every
// fixture owns disposable queues and a PostgreSQL schema, never production data.
type sqsFixture struct {
	ctx        context.Context
	pool       *pgxpool.Pool
	client     *awssqs.Client
	queue, dlq string
	service    *financial.Service
	consumer   *sqsadapter.Consumer
}

func realSQSClient(t *testing.T, ctx context.Context) *awssqs.Client {
	t.Helper()
	endpoint := os.Getenv("TEST_SQS_ENDPOINT")
	if endpoint == "" {
		t.Skip("set TEST_SQS_ENDPOINT and TEST_DATABASE_URL for PostgreSQL + LocalStack integration")
	}
	client, err := sqsadapter.NewClient(ctx, config.Config{AWSRegion: "us-east-1", SQSEndpoint: endpoint, AWSAccessKeyID: "test", AWSSecretAccessKey: "test"})
	must(t, err)
	return client
}
func newSQSFixture(t *testing.T) *sqsFixture {
	t.Helper()
	if os.Getenv("TEST_SQS_ENDPOINT") == "" {
		t.Skip("set TEST_SQS_ENDPOINT for real SQS integration")
	}
	pool, _ := phase5Pool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	client := realSQSClient(t, ctx)
	name := fmt.Sprintf("phase6-%d", time.Now().UnixNano())
	dlq, err := client.CreateQueue(ctx, &awssqs.CreateQueueInput{QueueName: aws.String(name + "-dlq.fifo"), Attributes: map[string]string{"FifoQueue": "true", "ContentBasedDeduplication": "false"}})
	must(t, err)
	attrs, err := client.GetQueueAttributes(ctx, &awssqs.GetQueueAttributesInput{QueueUrl: dlq.QueueUrl, AttributeNames: []types.QueueAttributeName{types.QueueAttributeNameQueueArn}})
	must(t, err)
	policy, err := json.Marshal(map[string]string{"deadLetterTargetArn": attrs.Attributes["QueueArn"], "maxReceiveCount": "3"})
	must(t, err)
	queue, err := client.CreateQueue(ctx, &awssqs.CreateQueueInput{QueueName: aws.String(name + ".fifo"), Attributes: map[string]string{"FifoQueue": "true", "ContentBasedDeduplication": "false", "VisibilityTimeout": "10", "RedrivePolicy": string(policy)}})
	must(t, err)
	f := &sqsFixture{ctx: ctx, pool: pool, client: client, queue: aws.ToString(queue.QueueUrl), dlq: aws.ToString(dlq.QueueUrl), service: financial.NewService(NewUnitOfWork(pool))}
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		for _, url := range []string{f.queue, f.dlq} {
			_, err := client.DeleteQueue(cleanup, &awssqs.DeleteQueueInput{QueueUrl: aws.String(url)})
			if err != nil {
				t.Errorf("queue cleanup: %v", err)
			}
		}
	})
	f.consumer = f.newConsumer(t, f.service)
	return f
}
func (f *sqsFixture) newConsumer(t *testing.T, service *financial.Service) *sqsadapter.Consumer {
	t.Helper()
	c, err := sqsadapter.NewConsumer(realSQSClient(t, f.ctx), service, sqsadapter.Options{QueueURL: f.queue, ConsumerName: "phase6-financial", WaitSeconds: 1, VisibilitySeconds: 10, MaxMessages: 1, Concurrency: 3, ProcessingTimeout: 5 * time.Second, AckTimeout: 3 * time.Second, ReceiveRetryDelay: 50 * time.Millisecond}, slog.New(slog.NewJSONHandler(io.Discard, nil)))
	must(t, err)
	return c
}
func (f *sqsFixture) envelope(t *testing.T, id wallet.ID, player, external string, kind wager.Type, amount int64, ref string) sqsadapter.Envelope {
	t.Helper()
	return sqsadapter.Envelope{SchemaVersion: 1, MessageID: "delivery-" + external, ProviderID: "provider", ExternalTransactionID: external, PlayerID: player, WalletID: string(id), Type: kind, Money: financialMoney(t, amount).External(), RoundID: "round", ReferenceExternalTransactionID: ref, CorrelationID: "correlation", CausationID: "cause-" + external, OccurredAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
}
func (f *sqsFixture) send(t *testing.T, e sqsadapter.Envelope, dedupOverride string) {
	t.Helper()
	in, err := sqsadapter.BuildSendInput(f.queue, e)
	must(t, err)
	if dedupOverride != "" {
		in.MessageDeduplicationId = aws.String(dedupOverride)
	}
	_, err = f.client.SendMessage(f.ctx, in)
	must(t, err)
}
func (f *sqsFixture) receive(t *testing.T, c *sqsadapter.Consumer) types.Message {
	t.Helper()
	ctx, cancel := context.WithTimeout(f.ctx, 5*time.Second)
	defer cancel()
	for ctx.Err() == nil {
		messages, err := c.Receive(ctx)
		must(t, err)
		if len(messages) != 0 {
			return messages[0]
		}
	}
	t.Fatal("message did not arrive before deadline")
	return types.Message{}
}
func (f *sqsFixture) visibleAgain(t *testing.T, m types.Message) {
	t.Helper()
	_, err := f.client.ChangeMessageVisibility(f.ctx, &awssqs.ChangeMessageVisibilityInput{QueueUrl: aws.String(f.queue), ReceiptHandle: m.ReceiptHandle, VisibilityTimeout: 0})
	must(t, err)
}
func (f *sqsFixture) completed(t *testing.T, e sqsadapter.Envelope) {
	t.Helper()
	record, err := NewRepositories(f.pool).Inbox.Get(f.ctx, "phase6-financial", e.MessageID)
	must(t, err)
	hash, err := e.Hash()
	must(t, err)
	if record.CompletedAt == nil || record.PayloadHash != hash || record.MessageID != e.MessageID {
		t.Fatal("incorrect durable Inbox", record)
	}
}
func (f *sqsFixture) verifyOneBet(t *testing.T, id wallet.ID, e sqsadapter.Envelope) {
	t.Helper()
	verifyWallet(t, f.ctx, f.pool, id, 8000, 2)
	countRows(t, f.ctx, f.pool, "wallet_ledger_entries", 2)
	countRows(t, f.ctx, f.pool, "wager_transactions", 2)
	countRows(t, f.ctx, f.pool, "outbox_events", 4)
	countRows(t, f.ctx, f.pool, "inbox_messages", 1)
	f.completed(t, e)
}
func (f *sqsFixture) empty(t *testing.T, queue string) {
	t.Helper()
	out, err := f.client.ReceiveMessage(f.ctx, &awssqs.ReceiveMessageInput{QueueUrl: aws.String(queue), WaitTimeSeconds: 1, MaxNumberOfMessages: 1})
	must(t, err)
	if len(out.Messages) != 0 {
		t.Fatal("queue contains unexpected visible message")
	}
	attrs, err := f.client.GetQueueAttributes(f.ctx, &awssqs.GetQueueAttributesInput{QueueUrl: aws.String(queue), AttributeNames: []types.QueueAttributeName{types.QueueAttributeNameApproximateNumberOfMessages, types.QueueAttributeNameApproximateNumberOfMessagesNotVisible}})
	must(t, err)
	if attrs.Attributes["ApproximateNumberOfMessages"] != "0" || attrs.Attributes["ApproximateNumberOfMessagesNotVisible"] != "0" {
		t.Fatal("queue still holds a delivery", attrs.Attributes)
	}
}

func TestIntegrationSQSProvisionedQueues(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	client := realSQSClient(t, ctx)
	var dlqARN string
	for _, name := range []string{"wager-transactions-dlq.fifo", "wager-transactions.fifo"} {
		queue, err := client.GetQueueUrl(ctx, &awssqs.GetQueueUrlInput{QueueName: aws.String(name)})
		must(t, err)
		attrs, err := client.GetQueueAttributes(ctx, &awssqs.GetQueueAttributesInput{QueueUrl: queue.QueueUrl, AttributeNames: []types.QueueAttributeName{types.QueueAttributeNameAll}})
		must(t, err)
		if attrs.Attributes["FifoQueue"] != "true" || attrs.Attributes["ContentBasedDeduplication"] != "false" {
			t.Fatal(attrs.Attributes)
		}
		if name == "wager-transactions-dlq.fifo" {
			dlqARN = attrs.Attributes["QueueArn"]
			continue
		}
		var redrive struct {
			DeadLetterTargetArn string `json:"deadLetterTargetArn"`
			MaxReceiveCount     string `json:"maxReceiveCount"`
		}
		must(t, json.Unmarshal([]byte(attrs.Attributes["RedrivePolicy"]), &redrive))
		if redrive.DeadLetterTargetArn != dlqARN || redrive.MaxReceiveCount != "5" || attrs.Attributes["VisibilityTimeout"] != "90" || attrs.Attributes["ReceiveMessageWaitTimeSeconds"] != "20" {
			t.Fatal(attrs.Attributes)
		}
	}
}

func TestIntegrationSQSAllFinancialPaths(t *testing.T) {
	f := newSQSFixture(t)
	w := createFinancialWallet(t, f.ctx, f.service, "player", 10000)
	for _, step := range []struct {
		external         string
		kind             wager.Type
		amount           int64
		ref              string
		balance, version int64
		state            wager.State
	}{
		{"bet", wager.TypeBet, 2000, "", 8000, 2, wager.StateProcessed},
		{"win", wager.TypeWin, 5000, "", 13000, 3, wager.StateProcessed},
		{"loss", wager.TypeLoss, 0, "", 13000, 3, wager.StateProcessed},
		{"refund", wager.TypeRefund, 2000, "bet", 15000, 4, wager.StateProcessed},
		{"rollback", wager.TypeRollback, 5000, "win", 10000, 5, wager.StateProcessed},
		{"rejected", wager.TypeBet, 20000, "", 10000, 5, wager.StateRejected},
		{"already-reversed", wager.TypeRefund, 2000, "bet", 10000, 5, wager.StateRejected},
		{"pending", wager.TypeRefund, 2000, "future-bet", 10000, 5, wager.StatePendingReference},
		{"future-bet", wager.TypeBet, 2000, "", 8000, 6, wager.StateProcessed},
	} {
		e := f.envelope(t, w.WalletID, "player", step.external, step.kind, step.amount, step.ref)
		f.send(t, e, "")
		m := f.receive(t, f.consumer)
		must(t, f.consumer.Handle(f.ctx, m))
		f.completed(t, e)
		tx, err := NewRepositories(f.pool).Wagers.GetByExternalID(f.ctx, "provider", wager.ExternalTransactionID(step.external))
		must(t, err)
		if tx.State() != step.state {
			t.Fatal("unexpected financial result", step.external, tx.State())
		}
		verifyWallet(t, f.ctx, f.pool, w.WalletID, step.balance, step.version)
	}
	handled, err := f.service.ResolvePendingOnce(f.ctx, time.Now().Add(2*time.Second))
	must(t, err)
	if !handled {
		t.Fatal("pending reference not resolved")
	}
	verifyWallet(t, f.ctx, f.pool, w.WalletID, 10000, 7)
	countRows(t, f.ctx, f.pool, "wallet_ledger_entries", 7)
	countRows(t, f.ctx, f.pool, "inbox_messages", 9)
	f.empty(t, f.queue)
	f.empty(t, f.dlq)
}

func TestIntegrationSQSCrashAndDeleteRecovery(t *testing.T) {
	for _, mode := range []string{"commit-before-delete", "before-commit", "inbox-completion-failure", "delete-failure"} {
		t.Run(mode, func(t *testing.T) {
			f := newSQSFixture(t)
			w := createFinancialWallet(t, f.ctx, f.service, "player", 10000)
			e := f.envelope(t, w.WalletID, "player", "bet", wager.TypeBet, 2000, "")
			f.send(t, e, "")
			m := f.receive(t, f.consumer)
			switch mode {
			case "commit-before-delete":
				result, err := f.consumer.Process(f.ctx, m)
				must(t, err)
				if result.Outcome != "PROCESSED" {
					t.Fatal(result)
				}
				f.verifyOneBet(t, w.WalletID, e)
			case "delete-failure":
				invalidReceipt := m
				invalidReceipt.ReceiptHandle = aws.String("invalid-receipt")
				if err := f.consumer.Handle(f.ctx, invalidReceipt); err == nil {
					t.Fatal("real SQS accepted an invalid receipt")
				}
				f.verifyOneBet(t, w.WalletID, e)
			case "before-commit", "inbox-completion-failure":
				if mode == "before-commit" {
					installOutboxFailure(t, f.ctx, f.pool)
				} else {
					_, err := f.pool.Exec(f.ctx, `CREATE FUNCTION test_fail_inbox() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected Inbox completion failure'; END $$; CREATE TRIGGER test_inbox_failure BEFORE UPDATE ON inbox_messages FOR EACH ROW EXECUTE FUNCTION test_fail_inbox();`)
					must(t, err)
				}
				err := f.consumer.Handle(f.ctx, m)
				if sqsadapter.Classify(err) != "transient" {
					t.Fatal("failure not retained for retry", err)
				}
				verifyWallet(t, f.ctx, f.pool, w.WalletID, 10000, 1)
				countRows(t, f.ctx, f.pool, "wallet_ledger_entries", 1)
				countRows(t, f.ctx, f.pool, "wager_transactions", 1)
				countRows(t, f.ctx, f.pool, "outbox_events", 2)
				countRows(t, f.ctx, f.pool, "inbox_messages", 0)
				trigger := "DROP TRIGGER test_outbox_failure ON outbox_events"
				if mode == "inbox-completion-failure" {
					trigger = "DROP TRIGGER test_inbox_failure ON inbox_messages"
				}
				_, err = f.pool.Exec(f.ctx, trigger)
				must(t, err)
			}
			// The original consumer disappears without ACK. Force its real receipt's
			// visibility to expire, then recover using an independent pool/client.
			f.visibleAgain(t, m)
			other := f.newConsumer(t, secondService(t, f.ctx, f.pool))
			redelivery := f.receive(t, other)
			if aws.ToString(redelivery.MessageId) != aws.ToString(m.MessageId) || redelivery.Attributes["ApproximateReceiveCount"] != "2" {
				t.Fatal("not a real SQS redelivery", redelivery.Attributes)
			}
			result, err := other.Process(f.ctx, redelivery)
			must(t, err)
			if (mode == "commit-before-delete" || mode == "delete-failure") && result.Outcome != "duplicate" {
				t.Fatal("committed delivery reapplied", result)
			}
			must(t, other.Handle(f.ctx, redelivery))
			f.verifyOneBet(t, w.WalletID, e)
			f.empty(t, f.queue)
		})
	}
}

func TestIntegrationSQSIncompleteInboxRecovery(t *testing.T) {
	for _, existingEffect := range []bool{false, true} {
		t.Run(fmt.Sprint(existingEffect), func(t *testing.T) {
			f := newSQSFixture(t)
			w := createFinancialWallet(t, f.ctx, f.service, "player", 10000)
			e := f.envelope(t, w.WalletID, "player", "bet", wager.TypeBet, 2000, "")
			if existingEffect {
				in, err := e.Input()
				must(t, err)
				_, err = f.service.ProcessHTTPWager(f.ctx, financial.HTTPWagerInput{WagerInput: in, IdempotencyKey: "existing"})
				must(t, err)
			}
			hash, err := e.Hash()
			must(t, err)
			must(t, NewRepositories(f.pool).Inbox.Create(f.ctx, ports.InboxMessage{ConsumerName: "phase6-financial", MessageID: e.MessageID, PayloadHash: hash, ReceivedAt: time.Now()}))
			f.send(t, e, "")
			m := f.receive(t, f.consumer)
			must(t, f.newConsumer(t, secondService(t, f.ctx, f.pool)).Handle(f.ctx, m))
			f.verifyOneBet(t, w.WalletID, e)
		})
	}
}

func TestIntegrationSQSPermanentFailuresReachRealDLQ(t *testing.T) {
	for _, mode := range []string{"invalid-json", "hash-conflict"} {
		t.Run(mode, func(t *testing.T) {
			f := newSQSFixture(t)
			w := createFinancialWallet(t, f.ctx, f.service, "player", 10000)
			e := f.envelope(t, w.WalletID, "player", "bet", wager.TypeBet, 2000, "")
			var originalHash string
			if mode == "hash-conflict" {
				f.send(t, e, "")
				must(t, f.consumer.Handle(f.ctx, f.receive(t, f.consumer)))
				originalHash, _ = e.Hash()
				e.Money = financialMoney(t, 3000).External()
				f.send(t, e, "changed-payload")
			} else {
				_, err := f.client.SendMessage(f.ctx, &awssqs.SendMessageInput{QueueUrl: aws.String(f.queue), MessageBody: aws.String(`{"schemaVersion":`), MessageGroupId: aws.String(sqsadapter.MessageGroupID(string(w.WalletID))), MessageDeduplicationId: aws.String("poison")})
				must(t, err)
			}
			var poisonBody string
			for attempt := 1; attempt <= 3; attempt++ {
				m := f.receive(t, f.consumer)
				poisonBody = aws.ToString(m.Body)
				if m.Attributes["ApproximateReceiveCount"] != fmt.Sprint(attempt) {
					t.Fatal(m.Attributes)
				}
				err := f.consumer.Handle(f.ctx, m)
				if sqsadapter.Classify(err) != "permanent" {
					t.Fatal("poison acknowledged", err)
				}
				if mode == "hash-conflict" && !errors.Is(err, financial.ErrDeliveryIntegrity) {
					t.Fatal(err)
				}
				f.visibleAgain(t, m)
			}
			// SQS performs the redrive on its next receive, not the application.
			messages, err := f.consumer.Receive(f.ctx)
			must(t, err)
			if len(messages) != 0 {
				t.Fatal("poison remained after receive limit")
			}
			ctx, cancel := context.WithTimeout(f.ctx, 5*time.Second)
			defer cancel()
			found := false
			for ctx.Err() == nil && !found {
				out, err := f.client.ReceiveMessage(ctx, &awssqs.ReceiveMessageInput{QueueUrl: aws.String(f.dlq), WaitTimeSeconds: 1, MaxNumberOfMessages: 1})
				must(t, err)
				if len(out.Messages) != 0 {
					found = true
					if aws.ToString(out.Messages[0].Body) != poisonBody {
						t.Fatal("redrive changed body")
					}
				}
			}
			if !found {
				t.Fatal("permanent failure did not reach real DLQ")
			}
			if mode == "hash-conflict" {
				verifyWallet(t, f.ctx, f.pool, w.WalletID, 8000, 2)
				record, err := NewRepositories(f.pool).Inbox.Get(f.ctx, "phase6-financial", e.MessageID)
				must(t, err)
				if record.PayloadHash != originalHash || record.CompletedAt == nil {
					t.Fatal("hash conflict overwrote Inbox")
				}
				countRows(t, f.ctx, f.pool, "wallet_ledger_entries", 2)
				countRows(t, f.ctx, f.pool, "outbox_events", 4)
			} else {
				verifyWallet(t, f.ctx, f.pool, w.WalletID, 10000, 1)
				countRows(t, f.ctx, f.pool, "inbox_messages", 0)
			}
		})
	}
}

func TestIntegrationSQSCrossTransport(t *testing.T) {
	f := newSQSFixture(t)
	w := createFinancialWallet(t, f.ctx, f.service, "player", 10000)
	e := f.envelope(t, w.WalletID, "player", "http-first", wager.TypeBet, 2000, "")
	in, err := e.Input()
	must(t, err)
	httpResult, err := f.service.ProcessHTTPWager(f.ctx, financial.HTTPWagerInput{WagerInput: in, IdempotencyKey: "http-key"})
	must(t, err)
	f.send(t, e, "")
	m := f.receive(t, f.consumer)
	result, err := f.consumer.Process(f.ctx, m)
	must(t, err)
	if result.Outcome != "external_duplicate" || result.Financial.TransactionID != httpResult.TransactionID {
		t.Fatal(result)
	}
	must(t, f.consumer.Handle(f.ctx, m))
	f.verifyOneBet(t, w.WalletID, e)
	e2 := f.envelope(t, w.WalletID, "player", "sqs-first", wager.TypeBet, 2000, "")
	f.send(t, e2, "")
	must(t, f.consumer.Handle(f.ctx, f.receive(t, f.consumer)))
	in, err = e2.Input()
	must(t, err)
	_, err = f.service.ProcessHTTPWager(f.ctx, financial.HTTPWagerInput{WagerInput: in, IdempotencyKey: "second-http-key"})
	expectError(t, err, financial.ErrDuplicateExternalTransaction)
	verifyWallet(t, f.ctx, f.pool, w.WalletID, 6000, 3)
	countRows(t, f.ctx, f.pool, "wallet_ledger_entries", 3)
	countRows(t, f.ctx, f.pool, "outbox_events", 6)
	// A distinct delivery cannot hide a changed business payload behind external uniqueness.
	e2.MessageID = "another-delivery"
	e2.Money = financialMoney(t, 3000).External()
	f.send(t, e2, "")
	err = f.consumer.Handle(f.ctx, f.receive(t, f.consumer))
	expectError(t, err, financial.ErrExternalPayloadConflict)
	countRows(t, f.ctx, f.pool, "inbox_messages", 2)
	verifyWallet(t, f.ctx, f.pool, w.WalletID, 6000, 3)
}

func TestIntegrationSQS50DuplicatesThreeConsumers(t *testing.T) {
	f := newSQSFixture(t)
	w := createFinancialWallet(t, f.ctx, f.service, "player", 10000)
	e := f.envelope(t, w.WalletID, "player", "bet", wager.TypeBet, 2000, "")
	consumers := []*sqsadapter.Consumer{f.consumer, f.newConsumer(t, secondService(t, f.ctx, f.pool)), f.newConsumer(t, secondService(t, f.ctx, f.pool))}
	// Fifty actual SQS messages deliberately use different FIFO dedup IDs while
	// retaining one durable envelope ID, so Inbox must absorb every duplicate.
	for i := 0; i < 50; i++ {
		f.send(t, e, fmt.Sprintf("attempt-%d", i))
	}
	m := f.receive(t, consumers[0])
	// Also exercise concurrent execution from independent pools using an actual
	// received envelope. FIFO itself serializes this wallet's transport deliveries.
	start := make(chan struct{})
	results := make(chan error, 50)
	var ready sync.WaitGroup
	ready.Add(50)
	for i := 0; i < 50; i++ {
		go func(c *sqsadapter.Consumer) { ready.Done(); <-start; _, err := c.Process(f.ctx, m); results <- err }(consumers[i%3])
	}
	ready.Wait()
	close(start)
	for i := 0; i < 50; i++ {
		must(t, <-results)
	}
	must(t, consumers[0].Handle(f.ctx, m))
	for i := 1; i < 50; i++ {
		c := consumers[i%3]
		must(t, c.Handle(f.ctx, f.receive(t, c)))
	}
	f.verifyOneBet(t, w.WalletID, e)
	f.empty(t, f.queue)
}

func TestIntegrationSQSFIFOOrdering(t *testing.T) {
	f := newSQSFixture(t)
	w := createFinancialWallet(t, f.ctx, f.service, "player", 0)
	for _, step := range []struct {
		id     string
		kind   wager.Type
		amount int64
	}{{"win", wager.TypeWin, 2000}, {"bet", wager.TypeBet, 2000}, {"loss", wager.TypeLoss, 0}} {
		f.send(t, f.envelope(t, w.WalletID, "player", step.id, step.kind, step.amount, ""), "")
	}
	// Receive the whole lane and exercise the production batch scheduler.
	batch, err := f.client.ReceiveMessage(f.ctx, &awssqs.ReceiveMessageInput{QueueUrl: aws.String(f.queue), MaxNumberOfMessages: 10, WaitTimeSeconds: 1, VisibilityTimeout: 10, MessageSystemAttributeNames: []types.MessageSystemAttributeName{types.MessageSystemAttributeNameAll}})
	must(t, err)
	if len(batch.Messages) != 3 {
		t.Fatalf("expected entire FIFO lane, got %d", len(batch.Messages))
	}
	for i, want := range []string{"win", "bet", "loss"} {
		parsed, _, err := sqsadapter.Parse(aws.ToString(batch.Messages[i].Body))
		must(t, err)
		if parsed.ExternalTransactionID != want {
			t.Fatal("SQS changed wallet ordering")
		}
	}
	f.consumer.ProcessBatch(f.ctx, f.ctx, batch.Messages)
	verifyWallet(t, f.ctx, f.pool, w.WalletID, 0, 3)
	bet, err := NewRepositories(f.pool).Wagers.GetByExternalID(f.ctx, "provider", "bet")
	must(t, err)
	if bet.State() != wager.StateProcessed {
		t.Fatal("BET ran before WIN")
	}
	countRows(t, f.ctx, f.pool, "inbox_messages", 3)
	f.empty(t, f.queue)
}

func TestIntegrationSQSThreeConsumersIndependentWalletsAndShutdown(t *testing.T) {
	f := newSQSFixture(t)
	consumers := []*sqsadapter.Consumer{f.consumer, f.newConsumer(t, secondService(t, f.ctx, f.pool)), f.newConsumer(t, secondService(t, f.ctx, f.pool))}
	wallets := make([]financial.CreateWalletResult, 3)
	messages := make([]types.Message, 3)
	for i := 0; i < 3; i++ {
		player := fmt.Sprintf("player-%d", i)
		wallets[i] = createFinancialWallet(t, f.ctx, f.service, player, 10000)
		f.send(t, f.envelope(t, wallets[i].WalletID, player, fmt.Sprintf("bet-%d", i), wager.TypeBet, 2000, ""), "")
		messages[i] = f.receive(t, consumers[i])
	}
	lock, err := f.pool.Begin(f.ctx)
	must(t, err)
	defer lock.Rollback(context.Background())
	_, err = lock.Exec(f.ctx, `SELECT id FROM wallets WHERE id=$1 FOR UPDATE`, string(wallets[0].WalletID))
	must(t, err)
	pollCtx, stopPolling := context.WithCancel(f.ctx)
	defer stopPolling()
	done := make(chan struct{})
	go func() { consumers[0].ProcessBatch(pollCtx, f.ctx, []types.Message{messages[0]}); close(done) }()
	// Wait for the actual SQL waiter, then stop polling while its financial work
	// remains active. Other consumers must progress despite this wallet lock.
	deadline, cancel := context.WithTimeout(f.ctx, 3*time.Second)
	defer cancel()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		var waiting bool
		must(t, f.pool.QueryRow(deadline, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE wait_event_type='Lock' AND query LIKE '%FROM wallets%' AND query LIKE '%FOR UPDATE%')`).Scan(&waiting))
		if waiting {
			break
		}
		select {
		case <-deadline.Done():
			t.Fatal("consumer never began financial lock")
		case <-ticker.C:
		}
	}
	stopPolling()
	errs := make(chan error, 2)
	for i := 1; i < 3; i++ {
		go func(i int) { errs <- consumers[i].Handle(f.ctx, messages[i]) }(i)
	}
	for i := 1; i < 3; i++ {
		must(t, <-errs)
	}
	for i := 1; i < 3; i++ {
		verifyWallet(t, f.ctx, f.pool, wallets[i].WalletID, 8000, 2)
	}
	select {
	case <-done:
		t.Fatal("locked operation unexpectedly ended")
	default:
	}
	must(t, lock.Commit(f.ctx))
	select {
	case <-done:
	case <-deadline.Done():
		t.Fatal("shutdown did not drain active commit")
	}
	for _, w := range wallets {
		verifyWallet(t, f.ctx, f.pool, w.WalletID, 8000, 2)
	}
	countRows(t, f.ctx, f.pool, "inbox_messages", 3)
	countRows(t, f.ctx, f.pool, "wallet_ledger_entries", 6)
	f.empty(t, f.queue)
}

func TestIntegrationSQSForcedShutdownRollsBackAndRetainsDelivery(t *testing.T) {
	f := newSQSFixture(t)
	w := createFinancialWallet(t, f.ctx, f.service, "player", 10000)
	e := f.envelope(t, w.WalletID, "player", "bet", wager.TypeBet, 2000, "")
	f.send(t, e, "")
	m := f.receive(t, f.consumer)
	lock, err := f.pool.Begin(f.ctx)
	must(t, err)
	defer lock.Rollback(context.Background())
	_, err = lock.Exec(f.ctx, `SELECT id FROM wallets WHERE id=$1 FOR UPDATE`, string(w.WalletID))
	must(t, err)
	work, cancelWork := context.WithCancel(f.ctx)
	defer cancelWork()
	poll, stopPoll := context.WithCancel(f.ctx)
	defer stopPoll()
	done := make(chan struct{})
	go func() { f.consumer.ProcessBatch(poll, work, []types.Message{m}); close(done) }()
	deadline, cancel := context.WithTimeout(f.ctx, 3*time.Second)
	defer cancel()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		var waiting bool
		must(t, f.pool.QueryRow(deadline, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE wait_event_type='Lock' AND query LIKE '%FROM wallets%' AND query LIKE '%FOR UPDATE%')`).Scan(&waiting))
		if waiting {
			break
		}
		select {
		case <-deadline.Done():
			t.Fatal("consumer never began its SQL work")
		case <-ticker.C:
		}
	}
	stopPoll()
	cancelWork()
	select {
	case <-done:
	case <-deadline.Done():
		t.Fatal("canceled work did not finish rollback")
	}
	must(t, lock.Commit(f.ctx))
	verifyWallet(t, f.ctx, f.pool, w.WalletID, 10000, 1)
	countRows(t, f.ctx, f.pool, "inbox_messages", 0)
	countRows(t, f.ctx, f.pool, "wallet_ledger_entries", 1)
	countRows(t, f.ctx, f.pool, "wager_transactions", 1)
	countRows(t, f.ctx, f.pool, "outbox_events", 2)
	f.visibleAgain(t, m)
	other := f.newConsumer(t, secondService(t, f.ctx, f.pool))
	must(t, other.Handle(f.ctx, f.receive(t, other)))
	f.verifyOneBet(t, w.WalletID, e)
}
