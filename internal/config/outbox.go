package config

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
)

func (c *Config) loadOutbox() error {
	var err error
	c.OutboxEnabled, err = strconv.ParseBool(getEnv("OUTBOX_PUBLISHER_ENABLED", "false"))
	if err != nil {
		return fmt.Errorf("invalid OUTBOX_PUBLISHER_ENABLED")
	}
	c.EventQueueName = getEnv("EVENT_QUEUE_NAME", "wager-events.fifo")
	c.EventQueueURL = getEnv("EVENT_QUEUE_URL", "")
	c.OutboxBatchSize = 10
	if c.OutboxBatchSize, err = int32Env("OUTBOX_BATCH_SIZE", c.OutboxBatchSize); err != nil {
		return err
	}
	for _, p := range []struct {
		key      string
		value    *time.Duration
		fallback time.Duration
	}{
		{"OUTBOX_POLL_INTERVAL", &c.OutboxPollInterval, time.Second},
		{"OUTBOX_BASE_RETRY_DELAY", &c.OutboxBaseRetryDelay, time.Second},
		{"OUTBOX_MAX_RETRY_DELAY", &c.OutboxMaxRetryDelay, time.Minute},
		{"OUTBOX_CLAIM_DURATION", &c.OutboxClaimDuration, 30 * time.Second},
		{"OUTBOX_PUBLISH_TIMEOUT", &c.OutboxPublishTimeout, 5 * time.Second},
		{"OUTBOX_STORE_TIMEOUT", &c.OutboxStoreTimeout, 3 * time.Second},
	} {
		if *p.value, err = durationEnv(p.key, p.fallback); err != nil {
			return err
		}
	}
	return nil
}
func (c Config) validateOutbox() error {
	if !c.OutboxEnabled {
		return nil
	}
	if strings.TrimSpace(c.AWSRegion) == "" || (c.AWSAccessKeyID == "") != (c.AWSSecretAccessKey == "") {
		return fmt.Errorf("invalid Outbox AWS configuration")
	}
	if !strings.HasSuffix(c.EventQueueName, ".fifo") || c.EventQueueName == c.SQSQueueName || c.EventQueueName == c.SQSDLQName {
		return fmt.Errorf("event FIFO queue must be separate from command queue and DLQ")
	}
	for _, raw := range []string{c.SQSEndpoint, c.EventQueueURL} {
		if raw != "" {
			u, err := url.Parse(raw)
			if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
				return fmt.Errorf("invalid Outbox endpoint or queue URL")
			}
		}
	}
	if c.EventQueueURL != "" && c.EventQueueURL == c.SQSQueueURL {
		return fmt.Errorf("event and command queue URLs must differ")
	}
	if c.OutboxPollInterval <= 0 || c.OutboxBatchSize < 1 || c.OutboxBatchSize > 100 || c.OutboxBaseRetryDelay < time.Microsecond || c.OutboxMaxRetryDelay < c.OutboxBaseRetryDelay || c.OutboxStoreTimeout <= 0 || c.OutboxPublishTimeout <= 0 || c.OutboxClaimDuration <= c.OutboxStoreTimeout || c.OutboxPublishTimeout >= c.OutboxClaimDuration-c.OutboxStoreTimeout {
		return fmt.Errorf("invalid Outbox polling/retry/lease budget")
	}
	return nil
}
