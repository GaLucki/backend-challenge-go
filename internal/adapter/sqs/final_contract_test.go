package sqs

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	awssqs "github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/junglegaming/backend-challenge-go/internal/application/financial"
)

const originalCommand = `{"messageId":"original-message","type":"WagerTransactionRequested","occurredAt":"2026-09-08T12:00:00Z","data":{"providerId":"provider-a","externalTransactionId":"original-bet","idempotencyKey":"shared-key","playerId":"player","walletId":"wallet","roundId":"round","gameId":"game","kind":"BET","money":{"amount":"20.00","currency":"BRL"}}}`

func TestOriginalEnvelopePreservesBusinessMetadataAndKey(t *testing.T) {
	e, in, err := Parse(originalCommand)
	if err != nil || in.GameID != "game" || e.IdempotencyKey != "shared-key" || e.CorrelationID != e.MessageID {
		t.Fatal(e, in, err)
	}
	hash, err := e.Hash()
	if err != nil {
		t.Fatal(err)
	}
	var shuffled map[string]any
	if err := json.Unmarshal([]byte(originalCommand), &shuffled); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.MarshalIndent(shuffled, "", " ")
	other, _, err := Parse(string(raw))
	otherHash, _ := other.Hash()
	if err != nil || hash != otherHash {
		t.Fatal("formatting changed inbox identity", err)
	}
	other.GameID = "other"
	changedHash, _ := other.Hash()
	if changedHash == hash {
		t.Fatal("game omitted from Inbox hash")
	}
}

type retryAPI struct {
	fakeAPI
	visibility int32
}

func (a *retryAPI) ChangeMessageVisibility(_ context.Context, in *awssqs.ChangeMessageVisibilityInput, _ ...func(*awssqs.Options)) (*awssqs.ChangeMessageVisibilityOutput, error) {
	a.visibility = in.VisibilityTimeout
	return &awssqs.ChangeMessageVisibilityOutput{}, nil
}

func TestTransientFailureSchedulesVisibilityBackoffWithoutDelete(t *testing.T) {
	api := &retryAPI{fakeAPI: fakeAPI{delete: func(context.Context, *awssqs.DeleteMessageInput) error {
		t.Error("ACK on transient failure")
		return nil
	}}}
	c, err := NewConsumer(api, processorFunc(func(context.Context, financial.DeliveryInput) (financial.DeliveryResult, error) {
		return financial.DeliveryResult{}, financial.ErrPersistence
	}), unitOptions(), quietLogger())
	if err != nil {
		t.Fatal(err)
	}
	message := unitMessage()
	message.Attributes["ApproximateReceiveCount"] = "4"
	if !errors.Is(c.Handle(context.Background(), message), financial.ErrPersistence) || api.visibility != 8 {
		t.Fatal("missing exponential retry", api.visibility)
	}
	if retryVisibility(time.Second, 90, 1000000) != 90 {
		t.Fatal("unbounded backoff")
	}
}
