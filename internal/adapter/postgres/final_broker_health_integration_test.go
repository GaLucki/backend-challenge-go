package postgres

import (
	"context"
	"os"
	"testing"
	"time"

	sqsadapter "github.com/junglegaming/backend-challenge-go/internal/adapter/sqs"
	"github.com/junglegaming/backend-challenge-go/internal/config"
)

func TestIntegrationReadinessProbesRealBrokerQueues(t *testing.T) {
	if os.Getenv("TEST_SQS_ENDPOINT") == "" {
		t.Skip("set TEST_SQS_ENDPOINT for real broker health integration")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cfg := config.Config{AWSRegion: "us-east-1", AWSAccessKeyID: "test", AWSSecretAccessKey: "test", SQSEndpoint: os.Getenv("TEST_SQS_ENDPOINT"), SQSEnabled: true, OutboxEnabled: true, SQSQueueName: "wager-transactions.fifo", EventQueueName: "wager-events.fifo"}
	health, err := sqsadapter.NewHealthPinger(ctx, cfg)
	must(t, err)
	must(t, health.Ping(ctx))
	cfg.SQSQueueName = "final-audit-does-not-exist.fifo"
	health, err = sqsadapter.NewHealthPinger(ctx, cfg)
	must(t, err)
	if health.Ping(ctx) == nil {
		t.Fatal("unprovisioned command queue reported ready")
	}
}
