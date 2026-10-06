package application

import (
	"context"
	"errors"
	"log/slog"

	"github.com/aws/aws-sdk-go-v2/aws"
	awssqs "github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
	sqsadapter "github.com/junglegaming/backend-challenge-go/internal/adapter/sqs"
	"github.com/junglegaming/backend-challenge-go/internal/application/outbox"
	"github.com/junglegaming/backend-challenge-go/internal/config"
	"github.com/junglegaming/backend-challenge-go/internal/ports"
	"go.uber.org/fx"
)

func RegisterOutboxPublisher(lc fx.Lifecycle, cfg config.Config, store ports.PublicationStore, logger *slog.Logger) {
	if !cfg.OutboxEnabled {
		return
	}
	var stopPolling, cancelWork context.CancelFunc
	done := make(chan struct{})
	lc.Append(fx.Hook{
		OnStart: func(ctx context.Context) error {
			client, err := sqsadapter.NewClient(ctx, cfg)
			if err != nil {
				return errors.New("Outbox SQS client configuration failed")
			}
			queueURL := cfg.EventQueueURL
			if queueURL == "" {
				q, err := client.GetQueueUrl(ctx, &awssqs.GetQueueUrlInput{QueueName: aws.String(cfg.EventQueueName)})
				if err != nil {
					return errors.New("Outbox event queue lookup failed")
				}
				queueURL = aws.ToString(q.QueueUrl)
			}
			attrs, err := client.GetQueueAttributes(ctx, &awssqs.GetQueueAttributesInput{QueueUrl: aws.String(queueURL), AttributeNames: []types.QueueAttributeName{types.QueueAttributeNameFifoQueue, types.QueueAttributeNameQueueArn}})
			if err != nil || attrs.Attributes["FifoQueue"] != "true" {
				return errors.New("Outbox event queue must be FIFO")
			}
			// Compare real ARNs as well as names/URLs, including endpoint aliases.
			for _, name := range []string{cfg.SQSQueueName, cfg.SQSDLQName} {
				url := cfg.SQSQueueURL
				if name == cfg.SQSDLQName || url == "" {
					q, err := client.GetQueueUrl(ctx, &awssqs.GetQueueUrlInput{QueueName: aws.String(name)})
					if err != nil {
						return errors.New("Outbox command queue separation lookup failed")
					}
					url = aws.ToString(q.QueueUrl)
				}
				a, err := client.GetQueueAttributes(ctx, &awssqs.GetQueueAttributesInput{QueueUrl: aws.String(url), AttributeNames: []types.QueueAttributeName{types.QueueAttributeNameQueueArn}})
				if err != nil {
					return errors.New("Outbox command queue separation validation failed")
				}
				if a.Attributes["QueueArn"] == attrs.Attributes["QueueArn"] {
					return errors.New("Outbox cannot publish into a command queue or command DLQ")
				}
			}
			id, err := outbox.NewIdentity()
			if err != nil {
				return err
			}
			publisher, err := outbox.NewPublisher(store, sqsadapter.NewEventSender(client, queueURL), outbox.Options{PublisherID: id, PollInterval: cfg.OutboxPollInterval, BatchSize: int(cfg.OutboxBatchSize), BaseRetryDelay: cfg.OutboxBaseRetryDelay, MaxRetryDelay: cfg.OutboxMaxRetryDelay, ClaimDuration: cfg.OutboxClaimDuration, PublishTimeout: cfg.OutboxPublishTimeout, StoreTimeout: cfg.OutboxStoreTimeout}, logger)
			if err != nil {
				return err
			}
			poll, pollCancel := context.WithCancel(context.Background())
			work, workCancel := context.WithCancel(context.Background())
			stopPolling, cancelWork = pollCancel, workCancel
			go func() { defer close(done); publisher.Run(poll, work) }()
			logger.InfoContext(ctx, "Outbox publisher started", "publisherId", id)
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
