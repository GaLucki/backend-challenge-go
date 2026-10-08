package sqs

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awssqs "github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/junglegaming/backend-challenge-go/internal/config"
)

// HealthPinger checks required command/event queues without receiving messages.
// A one-second cache bounds broker I/O when probes arrive concurrently.
type HealthPinger struct {
	mu      sync.Mutex
	client  *awssqs.Client
	queues  map[string]string
	checked time.Time
	lastErr error
}

func NewHealthPinger(ctx context.Context, cfg config.Config) (*HealthPinger, error) {
	client, err := NewClient(ctx, cfg)
	if err != nil {
		return nil, errors.New("broker health client configuration failed")
	}
	queues := map[string]string{}
	if cfg.SQSEnabled {
		queues[cfg.SQSQueueName] = cfg.SQSQueueURL
	}
	if cfg.OutboxEnabled {
		queues[cfg.EventQueueName] = cfg.EventQueueURL
	}
	return &HealthPinger{client: client, queues: queues}, nil
}

func (h *HealthPinger) Ping(ctx context.Context) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if time.Since(h.checked) < time.Second {
		return h.lastErr
	}
	var failure error
	for name, queue := range h.queues {
		if queue == "" {
			result, err := h.client.GetQueueUrl(ctx, &awssqs.GetQueueUrlInput{QueueName: aws.String(name)})
			if err != nil {
				failure = errors.New("broker unavailable")
				break
			}
			queue = aws.ToString(result.QueueUrl)
			h.queues[name] = queue
		}
		attrs, err := h.client.GetQueueAttributes(ctx, &awssqs.GetQueueAttributesInput{QueueUrl: aws.String(queue), AttributeNames: []types.QueueAttributeName{types.QueueAttributeNameFifoQueue}})
		if err != nil || attrs.Attributes["FifoQueue"] != "true" {
			h.queues[name] = ""
			failure = errors.New("broker unavailable")
			break
		}
	}
	h.checked, h.lastErr = time.Now(), failure
	return failure
}
