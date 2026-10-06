package sqs

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awssqs "github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/junglegaming/backend-challenge-go/internal/ports"
)

type sendAPI struct {
	in  *awssqs.SendMessageInput
	err error
}

func (s *sendAPI) SendMessage(_ context.Context, in *awssqs.SendMessageInput, _ ...func(*awssqs.Options)) (*awssqs.SendMessageOutput, error) {
	s.in = in
	return &awssqs.SendMessageOutput{}, s.err
}
func TestEventSendPreservesEnvelopeAndRetryIdentity(t *testing.T) {
	e := ports.OutboxEvent{EventID: "event", AggregateID: "wallet", EventType: "WalletBalanceChanged", OccurredAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), Payload: []byte(`{"version":1,"eventId":"event","aggregateId":"wallet","eventType":"WalletBalanceChanged","correlationId":"correlation","causationId":"transaction","occurredAt":"2026-01-01T00:00:00Z","data":{"money":{"amount":"25.00","currency":"BRL"}}}`)}
	api := &sendAPI{}
	sender := NewEventSender(api, "events")
	if err := sender.Send(context.Background(), e); err != nil {
		t.Fatal(err)
	}
	first := *api.in
	api.err = errors.New("SDK failure")
	e.RetryCount = 7
	if err := sender.Send(context.Background(), e); !errors.Is(err, api.err) {
		t.Fatal(err)
	}
	if aws.ToString(first.MessageBody) != string(e.Payload) || aws.ToString(api.in.MessageBody) != string(e.Payload) || aws.ToString(first.MessageDeduplicationId) != aws.ToString(api.in.MessageDeduplicationId) || aws.ToString(first.MessageGroupId) != EventMessageGroupID(e.AggregateID) {
		t.Fatal("payload/identity changed on retry")
	}
	if EventMessageGroupID("a") == EventMessageGroupID("b") || EventDeduplicationID("a") == EventDeduplicationID("b") {
		t.Fatal("global FIFO identity")
	}
	e.EventID = "different"
	if _, err := EventSendInput("events", e); !errors.Is(err, ErrInvalidEvent) {
		t.Fatal("mismatched envelope accepted")
	}
}
