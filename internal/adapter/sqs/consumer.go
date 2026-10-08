package sqs

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awssqs "github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/junglegaming/backend-challenge-go/internal/application/financial"
	"github.com/junglegaming/backend-challenge-go/internal/observability"
	"github.com/junglegaming/backend-challenge-go/internal/ports"
)

type API interface {
	ReceiveMessage(context.Context, *awssqs.ReceiveMessageInput, ...func(*awssqs.Options)) (*awssqs.ReceiveMessageOutput, error)
	DeleteMessage(context.Context, *awssqs.DeleteMessageInput, ...func(*awssqs.Options)) (*awssqs.DeleteMessageOutput, error)
}
type Processor interface {
	ProcessDelivery(context.Context, financial.DeliveryInput) (financial.DeliveryResult, error)
}
type Options struct {
	QueueURL          string
	ConsumerName      string
	WaitSeconds       int32
	VisibilitySeconds int32
	MaxMessages       int32
	Concurrency       int
	ProcessingTimeout time.Duration
	AckTimeout        time.Duration
	ReceiveRetryDelay time.Duration
}

func (o Options) Validate() error {
	if o.QueueURL == "" || o.ConsumerName == "" || o.WaitSeconds < 0 || o.WaitSeconds > 20 || o.VisibilitySeconds < 1 || o.VisibilitySeconds > 43200 || o.MaxMessages < 1 || o.MaxMessages > 10 || o.Concurrency < 1 || o.ProcessingTimeout <= 0 || o.AckTimeout <= 0 || o.ReceiveRetryDelay <= 0 {
		return fmt.Errorf("invalid SQS consumer options")
	}
	return nil
}

type Consumer struct {
	api       API
	processor Processor
	options   Options
	logger    *slog.Logger
	metrics   ports.Telemetry
}

func NewConsumer(api API, processor Processor, options Options, logger *slog.Logger) (*Consumer, error) {
	if err := options.Validate(); err != nil {
		return nil, err
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Consumer{api: api, processor: processor, options: options, logger: logger, metrics: ports.NoopTelemetry{}}, nil
}
func (c *Consumer) WithTelemetry(t ports.Telemetry) *Consumer {
	if t != nil {
		c.metrics = t
	}
	return c
}
func Classify(err error) string {
	if err == nil {
		return "accepted"
	}
	for _, permanent := range []error{ErrInvalidEnvelope, ErrInvalidGroup, financial.ErrDeliveryIntegrity, financial.ErrExternalPayloadConflict, financial.ErrIdempotencyConflict, financial.ErrDuplicateExternalTransaction, financial.ErrInvalidInput, financial.ErrInvalidAmount, financial.ErrInvalidOperationType, financial.ErrWalletNotFound, financial.ErrPlayerMismatch, financial.ErrCurrencyMismatch} {
		if errors.Is(err, permanent) {
			return "permanent"
		}
	}
	return "transient"
}

// Process deliberately has no ACK. It allows recovery tests to stop a consumer
// after a real database commit and before its SQS receipt is deleted.
func (c *Consumer) Process(ctx context.Context, message types.Message) (result financial.DeliveryResult, resultErr error) {
	started := time.Now()
	c.metrics.InFlight("sqs_in_flight", 1)
	defer func() {
		c.metrics.InFlight("sqs_in_flight", -1)
		c.metrics.Duration("sqs_processing_duration_seconds", time.Since(started))
		if resultErr != nil {
			c.metrics.Count("sqs_events_total", Classify(resultErr)+"_failure")
			if errors.Is(resultErr, financial.ErrPersistence) {
				c.metrics.Count("dependency_failures_total", "postgres", "consumer")
			}
		}
		if result.Outcome == "duplicate" || result.Outcome == "external_duplicate" || result.Outcome == "replay" {
			c.metrics.Count("sqs_events_total", "duplicate")
		}
	}()
	if err := ctx.Err(); err != nil {
		return financial.DeliveryResult{}, err
	}
	envelope, input, err := Parse(aws.ToString(message.Body))
	if err != nil {
		return financial.DeliveryResult{}, err
	}
	if message.Attributes[string(types.MessageSystemAttributeNameMessageGroupId)] != MessageGroupID(envelope.WalletID) {
		return financial.DeliveryResult{}, ErrInvalidGroup
	}
	hash, err := envelope.Hash()
	if err != nil {
		return financial.DeliveryResult{}, err
	}
	ctx = observability.WithCorrelationID(ctx, envelope.CorrelationID)
	ctx = observability.WithMessageID(ctx, envelope.MessageID)
	processCtx, cancel := context.WithTimeout(ctx, c.options.ProcessingTimeout)
	defer cancel()
	result, err = c.processor.ProcessDelivery(processCtx, financial.DeliveryInput{ConsumerName: c.options.ConsumerName, MessageID: envelope.MessageID, PayloadHash: hash, IdempotencyKey: envelope.IdempotencyKey, Wager: input})
	c.logger.InfoContext(ctx, "SQS delivery processed", "consumerName", c.options.ConsumerName, "messageId", envelope.MessageID, "sqsMessageId", aws.ToString(message.MessageId), "correlationId", envelope.CorrelationID, "transactionId", result.Financial.TransactionID, "walletId", envelope.WalletID, "providerId", envelope.ProviderID, "operationType", envelope.Type, "durationMs", time.Since(started).Milliseconds(), "receiveCount", message.Attributes[string(types.MessageSystemAttributeNameApproximateReceiveCount)], "outcome", result.Outcome, "failureCode", result.Financial.FailureCode, "classification", Classify(err))
	return result, err
}
func (c *Consumer) Handle(ctx context.Context, message types.Message) error {
	if _, err := c.Process(ctx, message); err != nil {
		if Classify(err) == "transient" {
			if api, ok := c.api.(interface {
				ChangeMessageVisibility(context.Context, *awssqs.ChangeMessageVisibilityInput, ...func(*awssqs.Options)) (*awssqs.ChangeMessageVisibilityOutput, error)
			}); ok {
				attempt, _ := strconv.ParseInt(message.Attributes["ApproximateReceiveCount"], 10, 32)
				retryCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), c.options.AckTimeout)
				_, retryErr := api.ChangeMessageVisibility(retryCtx, &awssqs.ChangeMessageVisibilityInput{QueueUrl: aws.String(c.options.QueueURL), ReceiptHandle: message.ReceiptHandle, VisibilityTimeout: retryVisibility(c.options.ReceiveRetryDelay, c.options.VisibilitySeconds, int32(attempt))})
				cancel()
				if retryErr != nil {
					c.metrics.Count("dependency_failures_total", "sqs", "consumer")
				}
			}
		}
		c.logger.WarnContext(ctx, "SQS delivery retained", "consumerName", c.options.ConsumerName, "sqsMessageId", aws.ToString(message.MessageId), "receiveCount", message.Attributes[string(types.MessageSystemAttributeNameApproximateReceiveCount)], "classification", Classify(err))
		return err
	}
	ackCtx, cancel := context.WithTimeout(ctx, c.options.AckTimeout)
	defer cancel()
	_, err := c.api.DeleteMessage(ackCtx, &awssqs.DeleteMessageInput{QueueUrl: aws.String(c.options.QueueURL), ReceiptHandle: message.ReceiptHandle})
	if err != nil {
		c.metrics.Count("sqs_events_total", "delete_failure")
		c.metrics.Count("dependency_failures_total", "sqs", "consumer")
		c.logger.WarnContext(ctx, "SQS acknowledgement failed", "consumerName", c.options.ConsumerName, "sqsMessageId", aws.ToString(message.MessageId))
	} else {
		c.metrics.Count("sqs_events_total", "completed")
	}
	return err
}

func retryVisibility(base time.Duration, maximum, attempt int32) int32 {
	seconds := max(int32(1), int32(min(base/time.Second, time.Duration(43200))))
	for n := int32(1); n < attempt && seconds < maximum; n++ {
		if seconds > maximum/2 {
			return maximum
		}
		seconds *= 2
	}
	return min(seconds, maximum)
}
func (c *Consumer) Receive(ctx context.Context) ([]types.Message, error) {
	out, err := c.api.ReceiveMessage(ctx, &awssqs.ReceiveMessageInput{QueueUrl: aws.String(c.options.QueueURL), WaitTimeSeconds: c.options.WaitSeconds, VisibilityTimeout: c.options.VisibilitySeconds, MaxNumberOfMessages: c.options.MaxMessages, MessageSystemAttributeNames: []types.MessageSystemAttributeName{types.MessageSystemAttributeNameMessageGroupId, types.MessageSystemAttributeNameMessageDeduplicationId, types.MessageSystemAttributeNameApproximateReceiveCount}})
	if err != nil {
		if ctx.Err() == nil {
			c.metrics.Count("sqs_events_total", "poll_failure")
			c.metrics.Count("dependency_failures_total", "sqs", "consumer")
		}
		return nil, err
	}
	for range out.Messages {
		c.metrics.Count("sqs_events_total", "received")
	}
	return out.Messages, nil
}

// ProcessBatch serializes each FIFO group and runs separate groups in parallel.
// Failure stops that group's tail; later messages cannot pass an unacknowledged head.
func (c *Consumer) ProcessBatch(pollCtx, workCtx context.Context, messages []types.Message) {
	groups := map[string][]types.Message{}
	for _, m := range messages {
		group := m.Attributes[string(types.MessageSystemAttributeNameMessageGroupId)]
		if group == "" {
			group = "invalid:" + aws.ToString(m.MessageId)
		}
		groups[group] = append(groups[group], m)
	}
	lanes := make(chan []types.Message, len(groups))
	for _, lane := range groups {
		lanes <- lane
	}
	close(lanes)
	var wg sync.WaitGroup
	workers := min(c.options.Concurrency, len(groups))
	wg.Add(workers)
	for i := 0; i < workers; i++ {
		go func() {
			defer wg.Done()
			for lane := range lanes {
				for _, message := range lane {
					if pollCtx.Err() != nil || workCtx.Err() != nil {
						return
					}
					if err := c.Handle(workCtx, message); err != nil {
						break
					}
				}
			}
		}()
	}
	wg.Wait()
}

// Poll cancellation stops new work; the separate work context drains active work.
func (c *Consumer) Run(pollCtx, workCtx context.Context) {
	for pollCtx.Err() == nil && workCtx.Err() == nil {
		messages, err := c.Receive(pollCtx)
		if err != nil {
			if pollCtx.Err() != nil {
				return
			}
			c.logger.WarnContext(pollCtx, "SQS receive failed", "consumerName", c.options.ConsumerName)
			timer := time.NewTimer(c.options.ReceiveRetryDelay)
			select {
			case <-pollCtx.Done():
				timer.Stop()
				return
			case <-workCtx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
			continue
		}
		if pollCtx.Err() != nil {
			return
		}
		c.ProcessBatch(pollCtx, workCtx, messages)
	}
}
