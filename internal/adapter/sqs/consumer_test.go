package sqs

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awssqs "github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/junglegaming/backend-challenge-go/internal/application/financial"
	"github.com/junglegaming/backend-challenge-go/internal/domain/money"
)

func testEnvelope() Envelope {
	return Envelope{SchemaVersion: 1, MessageID: "delivery", ProviderID: "provider", ExternalTransactionID: "external", PlayerID: "player", WalletID: "wallet", Type: "BET", Money: money.External{Amount: "20.00", Currency: "BRL"}, RoundID: "round", CorrelationID: "correlation", CausationID: "causation", OccurredAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
}
func TestContractCanonicalAndFIFO(t *testing.T) {
	e := testEnvelope()
	raw, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	hash, err := e.Hash()
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err = json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	fields["money"] = map[string]string{"currency": "BRL", "amount": "20"}
	formatted, err := json.MarshalIndent(fields, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	parsed, in, err := Parse(string(formatted))
	if err != nil || in.Amount.Cents() != 2000 {
		t.Fatal(err)
	}
	if other, err := parsed.Hash(); err != nil || other != hash {
		t.Fatal("transport formatting changed hash")
	}
	parsed.CorrelationID = "different"
	other, _ := parsed.Hash()
	if other == hash {
		t.Fatal("inbox metadata omitted")
	}
	e.OccurredAt = e.OccurredAt.In(time.FixedZone("offset", 3600))
	if other, _ := e.Hash(); other != hash {
		t.Fatal("timestamp offset changed canonical instant")
	}
	send, err := BuildSendInput("queue", e)
	if err != nil {
		t.Fatal(err)
	}
	if aws.ToString(send.MessageGroupId) != MessageGroupID(e.WalletID) || aws.ToString(send.MessageDeduplicationId) != MessageDeduplicationID(e.MessageID) {
		t.Fatal("wrong FIFO strategy")
	}
	if MessageGroupID("a") == MessageGroupID("b") || len(MessageGroupID(strings.Repeat("w", 1000))) > 128 || MessageDeduplicationID("a") == MessageDeduplicationID("b") {
		t.Fatal("invalid FIFO identity")
	}
}
func TestInvalidEnvelope(t *testing.T) {
	raw, _ := json.Marshal(testEnvelope())
	for _, body := range []string{"{", string(raw) + " {}", strings.Replace(string(raw), `"schemaVersion":1`, `"schemaVersion":2`, 1), strings.Replace(string(raw), `"amount":"20.00"`, `"amount":20.0`, 1), strings.Replace(string(raw), `"amount":"20.00"`, `"amount":"NaN"`, 1), strings.Replace(string(raw), `"type":"BET"`, `"type":"OPENING"`, 1), strings.Replace(string(raw), `"walletId":"wallet"`, `"walletId":""`, 1), strings.Replace(string(raw), `"schemaVersion":1`, `"extra":"secret","schemaVersion":1`, 1)} {
		if _, _, err := Parse(body); !errors.Is(err, ErrInvalidEnvelope) {
			t.Fatalf("invalid body accepted: %s", body)
		}
	}
}

type processorFunc func(context.Context, financial.DeliveryInput) (financial.DeliveryResult, error)

func (f processorFunc) ProcessDelivery(ctx context.Context, in financial.DeliveryInput) (financial.DeliveryResult, error) {
	return f(ctx, in)
}

type fakeAPI struct {
	receive func(context.Context) (*awssqs.ReceiveMessageOutput, error)
	delete  func(context.Context, *awssqs.DeleteMessageInput) error
}

func (f fakeAPI) ReceiveMessage(ctx context.Context, _ *awssqs.ReceiveMessageInput, _ ...func(*awssqs.Options)) (*awssqs.ReceiveMessageOutput, error) {
	return f.receive(ctx)
}
func (f fakeAPI) DeleteMessage(ctx context.Context, in *awssqs.DeleteMessageInput, _ ...func(*awssqs.Options)) (*awssqs.DeleteMessageOutput, error) {
	return &awssqs.DeleteMessageOutput{}, f.delete(ctx, in)
}
func unitOptions() Options {
	return Options{QueueURL: "queue", ConsumerName: "consumer", WaitSeconds: 20, VisibilitySeconds: 90, MaxMessages: 10, Concurrency: 4, ProcessingTimeout: time.Second, AckTimeout: time.Second, ReceiveRetryDelay: time.Second}
}
func unitMessage() types.Message {
	e := testEnvelope()
	raw, _ := json.Marshal(e)
	return types.Message{Body: aws.String(string(raw)), MessageId: aws.String("aws-message"), ReceiptHandle: aws.String("receipt"), Attributes: map[string]string{string(types.MessageSystemAttributeNameMessageGroupId): MessageGroupID(e.WalletID)}}
}
func quietLogger() *slog.Logger { return slog.New(slog.NewJSONHandler(io.Discard, nil)) }
func TestAckDecisionsAndClassification(t *testing.T) {
	for _, tc := range []struct {
		name, outcome         string
		processErr, deleteErr error
		wantAck               bool
		class                 string
	}{
		{"processed", "PROCESSED", nil, nil, true, "accepted"}, {"duplicate", "duplicate", nil, nil, true, "accepted"}, {"business rejected", "REJECTED", nil, nil, true, "accepted"}, {"pending", "PENDING_REFERENCE", nil, nil, true, "accepted"},
		{"transient", "", financial.ErrPersistence, nil, false, "transient"}, {"permanent", "", financial.ErrDeliveryIntegrity, nil, false, "permanent"}, {"delete failure", "PROCESSED", nil, errors.New("delete failed"), true, "transient"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var committed, acks atomic.Int32
			api := fakeAPI{delete: func(context.Context, *awssqs.DeleteMessageInput) error {
				if committed.Load() != 1 {
					t.Error("ACK before durable result")
				}
				acks.Add(1)
				return tc.deleteErr
			}}
			processor := processorFunc(func(context.Context, financial.DeliveryInput) (financial.DeliveryResult, error) {
				if tc.processErr == nil {
					committed.Store(1)
				}
				return financial.DeliveryResult{Outcome: tc.outcome}, tc.processErr
			})
			consumer, err := NewConsumer(api, processor, unitOptions(), quietLogger())
			if err != nil {
				t.Fatal(err)
			}
			err = consumer.Handle(context.Background(), unitMessage())
			if (acks.Load() == 1) != tc.wantAck || Classify(err) != tc.class {
				t.Fatalf("acks=%d class=%s", acks.Load(), Classify(err))
			}
		})
	}
}
func TestGracefulPollCancellationDrainsOnlyActiveMessage(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	var calls, acks atomic.Int32
	var received atomic.Bool
	api := fakeAPI{receive: func(ctx context.Context) (*awssqs.ReceiveMessageOutput, error) {
		if !received.Swap(true) {
			return &awssqs.ReceiveMessageOutput{Messages: []types.Message{unitMessage(), unitMessage()}}, nil
		}
		<-ctx.Done()
		return nil, ctx.Err()
	}, delete: func(context.Context, *awssqs.DeleteMessageInput) error { acks.Add(1); return nil }}
	processor := processorFunc(func(ctx context.Context, _ financial.DeliveryInput) (financial.DeliveryResult, error) {
		calls.Add(1)
		close(started)
		select {
		case <-release:
			return financial.DeliveryResult{Outcome: "PROCESSED"}, nil
		case <-ctx.Done():
			return financial.DeliveryResult{}, ctx.Err()
		}
	})
	consumer, err := NewConsumer(api, processor, unitOptions(), quietLogger())
	if err != nil {
		t.Fatal(err)
	}
	poll, cancelPoll := context.WithCancel(context.Background())
	work, cancelWork := context.WithCancel(context.Background())
	defer cancelWork()
	done := make(chan struct{})
	go func() { defer close(done); consumer.Run(poll, work) }()
	<-started
	cancelPoll()
	select {
	case <-done:
		t.Fatal("active work did not drain")
	default:
	}
	close(release)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("consumer did not stop")
	}
	if calls.Load() != 1 || acks.Load() != 1 {
		t.Fatal("new work started after stop")
	}
}
func TestGroupTailStopsOnFailure(t *testing.T) {
	var calls atomic.Int32
	consumer, err := NewConsumer(fakeAPI{}, processorFunc(func(context.Context, financial.DeliveryInput) (financial.DeliveryResult, error) {
		calls.Add(1)
		return financial.DeliveryResult{}, financial.ErrPersistence
	}), unitOptions(), quietLogger())
	if err != nil {
		t.Fatal(err)
	}
	consumer.ProcessBatch(context.Background(), context.Background(), []types.Message{unitMessage(), unitMessage()})
	if calls.Load() != 1 {
		t.Fatal("failed FIFO head was passed")
	}
}

func TestPermanentTransportValidationNeverProcessesOrDeletes(t *testing.T) {
	for _, invalidBody := range []bool{false, true} {
		message := unitMessage()
		if invalidBody {
			message.Body = aws.String(`{"schemaVersion":`)
		} else {
			message.Attributes["MessageGroupId"] = MessageGroupID("wrong-wallet")
		}
		consumer, err := NewConsumer(fakeAPI{delete: func(context.Context, *awssqs.DeleteMessageInput) error {
			t.Error("invalid delivery was deleted")
			return nil
		}}, processorFunc(func(context.Context, financial.DeliveryInput) (financial.DeliveryResult, error) {
			t.Error("invalid transport reached finance")
			return financial.DeliveryResult{}, nil
		}), unitOptions(), quietLogger())
		if err != nil {
			t.Fatal(err)
		}
		if Classify(consumer.Handle(context.Background(), message)) != "permanent" {
			t.Fatal("transport failure classified incorrectly")
		}
	}
}
