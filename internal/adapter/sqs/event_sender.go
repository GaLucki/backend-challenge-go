package sqs

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awssqs "github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/junglegaming/backend-challenge-go/internal/ports"
)

var ErrInvalidEvent = errors.New("invalid persisted event envelope")

type SendAPI interface {
	SendMessage(context.Context, *awssqs.SendMessageInput, ...func(*awssqs.Options)) (*awssqs.SendMessageOutput, error)
}
type EventSender struct {
	api      SendAPI
	queueURL string
}

func NewEventSender(api SendAPI, queueURL string) *EventSender {
	return &EventSender{api: api, queueURL: queueURL}
}
func EventMessageGroupID(aggregateID string) string { return identity("aggregate:", aggregateID) }
func EventDeduplicationID(eventID string) string    { return identity("event:", eventID) }
func EventSendInput(queueURL string, e ports.OutboxEvent) (*awssqs.SendMessageInput, error) {
	var envelope struct {
		EventID     string          `json:"eventId"`
		EventType   string          `json:"eventType"`
		AggregateID string          `json:"aggregateId"`
		OccurredAt  time.Time       `json:"occurredAt"`
		Version     int             `json:"version"`
		Data        json.RawMessage `json:"data"`
	}
	if queueURL == "" || json.Unmarshal(e.Payload, &envelope) != nil || e.EventID == "" || e.AggregateID == "" || e.EventType == "" || envelope.EventID != e.EventID || envelope.EventType != e.EventType || envelope.AggregateID != e.AggregateID || envelope.Version != 1 || envelope.OccurredAt.IsZero() || !envelope.OccurredAt.Equal(e.OccurredAt) || len(envelope.Data) == 0 {
		return nil, ErrInvalidEvent
	}
	// Publish the JSONB payload exactly as read. No reconstruction, new ID or float.
	return &awssqs.SendMessageInput{QueueUrl: aws.String(queueURL), MessageBody: aws.String(string(e.Payload)), MessageGroupId: aws.String(EventMessageGroupID(e.AggregateID)), MessageDeduplicationId: aws.String(EventDeduplicationID(e.EventID))}, nil
}
func (s *EventSender) Send(ctx context.Context, e ports.OutboxEvent) error {
	in, err := EventSendInput(s.queueURL, e)
	if err != nil {
		return err
	}
	_, err = s.api.SendMessage(ctx, in)
	return err
}
