package application

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awssqs "github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
	sqsadapter "github.com/junglegaming/backend-challenge-go/internal/adapter/sqs"
	"github.com/junglegaming/backend-challenge-go/internal/application/financial"
	"github.com/junglegaming/backend-challenge-go/internal/config"
	"go.uber.org/fx"
)

// RegisterSQSConsumer owns polling and draining before the shared SQL pool stops.
func RegisterSQSConsumer(lc fx.Lifecycle, cfg config.Config, service *financial.Service, logger *slog.Logger) {
	if !cfg.SQSEnabled {
		return
	}
	var stopPolling, cancelWork context.CancelFunc
	done := make(chan struct{})
	lc.Append(fx.Hook{
		OnStart: func(ctx context.Context) error {
			client, err := sqsadapter.NewClient(ctx, cfg)
			if err != nil {
				return errors.New("SQS client configuration failed")
			}
			queueURL := cfg.SQSQueueURL
			if queueURL == "" {
				queue, err := client.GetQueueUrl(ctx, &awssqs.GetQueueUrlInput{QueueName: aws.String(cfg.SQSQueueName)})
				if err != nil {
					return errors.New("SQS queue lookup failed")
				}
				queueURL = aws.ToString(queue.QueueUrl)
			}
			attributes, err := client.GetQueueAttributes(ctx, &awssqs.GetQueueAttributesInput{QueueUrl: aws.String(queueURL), AttributeNames: []types.QueueAttributeName{types.QueueAttributeNameFifoQueue, types.QueueAttributeNameRedrivePolicy}})
			if err != nil || attributes.Attributes[string(types.QueueAttributeNameFifoQueue)] != "true" || attributes.Attributes[string(types.QueueAttributeNameRedrivePolicy)] == "" {
				return errors.New("SQS queue must be FIFO with a redrive policy")
			}
			deadLetter, err := client.GetQueueUrl(ctx, &awssqs.GetQueueUrlInput{QueueName: aws.String(cfg.SQSDLQName)})
			if err != nil {
				return errors.New("SQS DLQ lookup failed")
			}
			dlqAttributes, err := client.GetQueueAttributes(ctx, &awssqs.GetQueueAttributesInput{QueueUrl: deadLetter.QueueUrl, AttributeNames: []types.QueueAttributeName{types.QueueAttributeNameFifoQueue, types.QueueAttributeNameQueueArn}})
			if err != nil || dlqAttributes.Attributes["FifoQueue"] != "true" {
				return errors.New("SQS DLQ must be FIFO")
			}
			var redrive struct {
				Target string `json:"deadLetterTargetArn"`
			}
			if err := json.Unmarshal([]byte(attributes.Attributes["RedrivePolicy"]), &redrive); err != nil || redrive.Target != dlqAttributes.Attributes["QueueArn"] {
				return errors.New("SQS redrive target does not match configured DLQ")
			}
			consumer, err := sqsadapter.NewConsumer(client, service, sqsadapter.Options{QueueURL: queueURL, ConsumerName: cfg.SQSConsumerName, WaitSeconds: cfg.SQSWaitSeconds, VisibilitySeconds: cfg.SQSVisibilitySeconds, MaxMessages: cfg.SQSMaxMessages, Concurrency: int(cfg.SQSConcurrency), ProcessingTimeout: cfg.SQSProcessingTimeout, AckTimeout: 3 * time.Second, ReceiveRetryDelay: time.Second}, logger)
			if err != nil {
				return err
			}
			pollCtx, pollCancel := context.WithCancel(context.Background())
			workCtx, workCancel := context.WithCancel(context.Background())
			stopPolling, cancelWork = pollCancel, workCancel
			go func() { defer close(done); consumer.Run(pollCtx, workCtx) }()
			logger.InfoContext(ctx, "SQS consumer started", "consumerName", cfg.SQSConsumerName)
			return nil
		},
		OnStop: func(ctx context.Context) error {
			stopPolling()
			defer cancelWork()
			select {
			case <-done:
				return nil
			case <-ctx.Done():
				cancelWork()
				<-done
				return ctx.Err()
			}
		},
	})
}
