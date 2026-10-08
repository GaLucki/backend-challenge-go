package application

import (
	"context"
	"errors"
	"log/slog"
	"strconv"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awssqs "github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/junglegaming/backend-challenge-go/internal/adapter/postgres"
	sqsadapter "github.com/junglegaming/backend-challenge-go/internal/adapter/sqs"
	"github.com/junglegaming/backend-challenge-go/internal/config"
	"github.com/junglegaming/backend-challenge-go/internal/observability"
	"github.com/junglegaming/backend-challenge-go/internal/ports"
	"go.uber.org/fx"
)

type OperationalCollector struct {
	mu      sync.Mutex
	reader  ports.OperationalReader
	metrics *observability.Metrics
	cfg     config.Config
	logger  *slog.Logger
	dlq     func(context.Context) (int64, error)
}

func NewOperationalCollector(reader ports.OperationalReader, metrics *observability.Metrics, cfg config.Config, pool *pgxpool.Pool, logger *slog.Logger) *OperationalCollector {
	if metrics.Enabled {
		metrics.Registry.MustRegister(postgres.NewPoolCollector(pool))
	}
	return &OperationalCollector{reader: reader, metrics: metrics, cfg: cfg, logger: logger}
}

// Collection errors retain the previous sample and publish freshness/success.
// Scraping never issues SQL or SQS calls; this bounded loop owns all external I/O.
func (c *OperationalCollector) CollectOnce(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.metrics.Enabled {
		return nil
	}
	dbCtx, cancel := context.WithTimeout(ctx, c.cfg.MetricsCollectTimeout)
	s, err := c.reader.ReadOperationalSnapshot(dbCtx)
	cancel()
	if err != nil {
		c.metrics.Set("operational_collection_success", 0)
		if ctx.Err() == nil {
			c.metrics.Count("dependency_failures_total", "postgres", "metrics")
			c.logger.WarnContext(ctx, "operational metrics collection failed", "dependency", "postgres")
		}
	} else {
		c.metrics.Set("outbox_pending", float64(s.OutboxPending))
		c.metrics.Set("outbox_retry_scheduled", float64(s.OutboxRetryScheduled))
		c.metrics.Set("outbox_lag_seconds", s.OutboxLag.Seconds())
		c.metrics.Set("pending_references", float64(s.PendingReferences))
		c.metrics.Set("operational_collection_success", 1)
		c.metrics.Set("operational_collection_timestamp_seconds", float64(time.Now().Unix()))
	}
	if c.dlq != nil {
		sqsCtx, stop := context.WithTimeout(ctx, c.cfg.MetricsCollectTimeout)
		depth, e := c.dlq(sqsCtx)
		stop()
		if e != nil {
			c.metrics.Set("dlq_collection_success", 0)
			if ctx.Err() == nil {
				c.metrics.Count("dependency_failures_total", "sqs", "metrics")
				c.logger.WarnContext(ctx, "operational metrics collection failed", "dependency", "sqs")
			}
			err = errors.Join(err, e)
		} else {
			c.metrics.Set("dlq_depth_approximate", float64(depth))
			c.metrics.Set("dlq_collection_success", 1)
			c.metrics.Set("dlq_collection_timestamp_seconds", float64(time.Now().Unix()))
		}
	}
	return err
}

func RegisterOperationalCollector(lc fx.Lifecycle, c *OperationalCollector) {
	if !c.metrics.Enabled {
		return
	}
	var cancel context.CancelFunc
	done := make(chan struct{})
	lc.Append(fx.Hook{OnStart: func(ctx context.Context) error {
		if c.cfg.SQSEnabled {
			client, err := sqsadapter.NewClient(ctx, c.cfg)
			if err != nil {
				return errors.New("metrics SQS client configuration failed")
			}
			c.metrics.Set("dlq_collection_enabled", 1)
			var queueURL string
			c.dlq = func(ctx context.Context) (int64, error) {
				if queueURL == "" {
					q, err := client.GetQueueUrl(ctx, &awssqs.GetQueueUrlInput{QueueName: aws.String(c.cfg.SQSDLQName)})
					if err != nil {
						return 0, err
					}
					queueURL = aws.ToString(q.QueueUrl)
				}
				attrs, err := client.GetQueueAttributes(ctx, &awssqs.GetQueueAttributesInput{QueueUrl: aws.String(queueURL), AttributeNames: []types.QueueAttributeName{types.QueueAttributeNameApproximateNumberOfMessages, types.QueueAttributeNameApproximateNumberOfMessagesNotVisible}})
				if err != nil {
					queueURL = ""
					return 0, err
				}
				visible, e1 := strconv.ParseInt(attrs.Attributes["ApproximateNumberOfMessages"], 10, 64)
				hidden, e2 := strconv.ParseInt(attrs.Attributes["ApproximateNumberOfMessagesNotVisible"], 10, 64)
				if e1 != nil || e2 != nil || visible < 0 || hidden < 0 || visible > 1<<63-1-hidden {
					return 0, errors.New("invalid DLQ depth attributes")
				}
				return visible + hidden, nil
			}
		}
		runCtx, stop := context.WithCancel(context.Background())
		cancel = stop
		go func() {
			defer close(done)
			ticker := time.NewTicker(c.cfg.MetricsCollectInterval)
			defer ticker.Stop()
			for {
				if runCtx.Err() != nil {
					return
				}
				_ = c.CollectOnce(runCtx)
				select {
				case <-runCtx.Done():
					return
				case <-ticker.C:
				}
			}
		}()
		return nil
	}, OnStop: func(ctx context.Context) error { cancel(); <-done; return ctx.Err() }})
}
