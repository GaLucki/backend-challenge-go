package config

import (
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	defaultAppEnv            = "development"
	defaultHTTPPort          = "8080"
	defaultLogLevel          = "info"
	defaultShutdownTimeout   = 10 * time.Second
	defaultDBMaxConns        = int32(10)
	defaultDBMinConns        = int32(1)
	defaultDBMaxConnLifetime = time.Hour
	defaultDBMaxConnIdleTime = 30 * time.Minute
	defaultDBConnectTimeout  = 5 * time.Second
	defaultDBHealthTimeout   = 2 * time.Second
)

// Config holds application configuration loaded from environment variables.
type Config struct {
	MetricsEnabled                                bool
	MetricsCollectInterval, MetricsCollectTimeout time.Duration
	AppEnv                                        string
	HTTPPort                                      string
	LogLevel                                      string
	ShutdownTimeout                               time.Duration

	DatabaseURL         string
	DBMaxConns          int32
	DBMinConns          int32
	DBMaxConnLifetime   time.Duration
	DBMaxConnIdleTime   time.Duration
	DBConnectTimeout    time.Duration
	DBHealthTimeout     time.Duration
	PendingPollInterval time.Duration
	PendingBaseDelay    time.Duration
	PendingMaxDelay     time.Duration
	PendingMaxAttempts  int32
	PendingTTL          time.Duration
	PendingBatchSize    int32

	// KeycloakURL is retained for legacy config compatibility; OIDC uses OIDCIssuerURL.
	KeycloakURL                                                   string
	SQSEndpoint                                                   string
	AWSRegion                                                     string
	AWSAccessKeyID                                                string
	AWSSecretAccessKey                                            string
	AWSSessionToken                                               string
	SQSEnabled                                                    bool
	SQSQueueName                                                  string
	SQSQueueURL                                                   string
	SQSDLQName                                                    string
	SQSConsumerName                                               string
	SQSWaitSeconds                                                int32
	SQSVisibilitySeconds                                          int32
	SQSMaxMessages                                                int32
	SQSConcurrency                                                int32
	SQSProcessingTimeout                                          time.Duration
	OutboxEnabled                                                 bool
	EventQueueName, EventQueueURL                                 string
	OutboxBatchSize                                               int32
	OutboxPollInterval, OutboxBaseRetryDelay, OutboxMaxRetryDelay time.Duration
	OutboxClaimDuration, OutboxPublishTimeout, OutboxStoreTimeout time.Duration
	OIDCEnabled                                                   bool
	OIDCIssuerURL, OIDCAudience                                   string
	OIDCHTTPTimeout                                               time.Duration
}

// Load reads configuration from environment variables and validates required fields.
func Load() (Config, error) {
	cfg := Config{
		AppEnv:              getEnv("APP_ENV", defaultAppEnv),
		HTTPPort:            getEnv("HTTP_PORT", defaultHTTPPort),
		LogLevel:            strings.ToLower(getEnv("LOG_LEVEL", defaultLogLevel)),
		ShutdownTimeout:     defaultShutdownTimeout,
		DatabaseURL:         strings.TrimSpace(os.Getenv("DATABASE_URL")),
		DBMaxConns:          defaultDBMaxConns,
		DBMinConns:          defaultDBMinConns,
		DBMaxConnLifetime:   defaultDBMaxConnLifetime,
		DBMaxConnIdleTime:   defaultDBMaxConnIdleTime,
		DBConnectTimeout:    defaultDBConnectTimeout,
		DBHealthTimeout:     defaultDBHealthTimeout,
		PendingPollInterval: time.Second,
		PendingBaseDelay:    time.Second,
		PendingMaxDelay:     time.Minute,
		PendingMaxAttempts:  10,
		PendingTTL:          15 * time.Minute,
		PendingBatchSize:    50,
		KeycloakURL:         os.Getenv("KEYCLOAK_URL"),
		SQSEndpoint:         os.Getenv("SQS_ENDPOINT"),
		AWSRegion:           getEnv("AWS_REGION", "us-east-1"),
		AWSAccessKeyID:      os.Getenv("AWS_ACCESS_KEY_ID"),
		AWSSecretAccessKey:  os.Getenv("AWS_SECRET_ACCESS_KEY"),
		AWSSessionToken:     os.Getenv("AWS_SESSION_TOKEN"),
		SQSQueueName:        getEnv("SQS_QUEUE_NAME", "wager-transactions.fifo"),
		SQSQueueURL:         getEnv("SQS_QUEUE_URL", ""),
		SQSDLQName:          getEnv("SQS_DLQ_NAME", "wager-transactions-dlq.fifo"),
		SQSConsumerName:     getEnv("SQS_CONSUMER_NAME", "wager-financial-v1"),
		SQSWaitSeconds:      20, SQSVisibilitySeconds: 90, SQSMaxMessages: 10, SQSConcurrency: 4, SQSProcessingTimeout: 5 * time.Second,
	}

	var err error
	if err = cfg.loadMetrics(); err != nil {
		return Config{}, err
	}
	if err = cfg.loadOIDC(); err != nil {
		return Config{}, err
	}
	if err = cfg.loadOutbox(); err != nil {
		return Config{}, err
	}
	if raw := getEnv("SQS_ENABLED", "false"); raw != "" {
		if cfg.SQSEnabled, err = strconv.ParseBool(raw); err != nil {
			return Config{}, fmt.Errorf("invalid SQS_ENABLED")
		}
	}
	for _, item := range []struct {
		key    string
		target *int32
	}{
		{"SQS_WAIT_SECONDS", &cfg.SQSWaitSeconds}, {"SQS_VISIBILITY_SECONDS", &cfg.SQSVisibilitySeconds}, {"SQS_MAX_MESSAGES", &cfg.SQSMaxMessages}, {"SQS_CONCURRENCY", &cfg.SQSConcurrency},
	} {
		if *item.target, err = int32Env(item.key, *item.target); err != nil {
			return Config{}, err
		}
	}
	if cfg.SQSProcessingTimeout, err = durationEnv("SQS_PROCESSING_TIMEOUT", cfg.SQSProcessingTimeout); err != nil {
		return Config{}, err
	}

	if cfg.ShutdownTimeout, err = durationEnv("SHUTDOWN_TIMEOUT", cfg.ShutdownTimeout); err != nil {
		return Config{}, err
	}
	if cfg.DBMaxConns, err = int32Env("DB_MAX_CONNS", cfg.DBMaxConns); err != nil {
		return Config{}, err
	}
	if cfg.DBMinConns, err = int32Env("DB_MIN_CONNS", cfg.DBMinConns); err != nil {
		return Config{}, err
	}
	if cfg.DBMaxConnLifetime, err = durationEnv("DB_MAX_CONN_LIFETIME", cfg.DBMaxConnLifetime); err != nil {
		return Config{}, err
	}
	if cfg.DBMaxConnIdleTime, err = durationEnv("DB_MAX_CONN_IDLE_TIME", cfg.DBMaxConnIdleTime); err != nil {
		return Config{}, err
	}
	if cfg.DBConnectTimeout, err = durationEnv("DB_CONNECT_TIMEOUT", cfg.DBConnectTimeout); err != nil {
		return Config{}, err
	}
	if cfg.DBHealthTimeout, err = durationEnv("DB_HEALTH_TIMEOUT", cfg.DBHealthTimeout); err != nil {
		return Config{}, err
	}
	for _, item := range []struct {
		key    string
		target *time.Duration
	}{
		{"PENDING_REFERENCE_POLL_INTERVAL", &cfg.PendingPollInterval},
		{"PENDING_REFERENCE_BASE_DELAY", &cfg.PendingBaseDelay},
		{"PENDING_REFERENCE_MAX_DELAY", &cfg.PendingMaxDelay},
		{"PENDING_REFERENCE_TTL", &cfg.PendingTTL},
	} {
		if *item.target, err = durationEnv(item.key, *item.target); err != nil {
			return Config{}, err
		}
	}
	if cfg.PendingMaxAttempts, err = int32Env("PENDING_REFERENCE_MAX_ATTEMPTS", cfg.PendingMaxAttempts); err != nil {
		return Config{}, err
	}
	if cfg.PendingBatchSize, err = int32Env("PENDING_REFERENCE_BATCH_SIZE", cfg.PendingBatchSize); err != nil {
		return Config{}, err
	}

	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}

	return cfg, nil
}

// Validate checks required configuration values.
func (c Config) Validate() error {
	if err := c.validateMetrics(); err != nil {
		return err
	}
	if strings.TrimSpace(c.AppEnv) == "" {
		return fmt.Errorf("APP_ENV is required")
	}

	if strings.TrimSpace(c.HTTPPort) == "" {
		return fmt.Errorf("HTTP_PORT is required")
	}

	port, err := strconv.Atoi(c.HTTPPort)
	if err != nil {
		return fmt.Errorf("HTTP_PORT must be a valid integer")
	}
	if port < 1 || port > 65535 {
		return fmt.Errorf("HTTP_PORT must be between 1 and 65535")
	}

	switch c.LogLevel {
	case "debug", "info", "warn", "error":
	default:
		return fmt.Errorf("LOG_LEVEL must be one of: debug, info, warn, error")
	}

	if c.ShutdownTimeout <= 0 {
		return fmt.Errorf("SHUTDOWN_TIMEOUT must be positive")
	}

	if err := validateDatabaseURL(c.DatabaseURL); err != nil {
		return err
	}

	if c.DBMaxConns < 1 {
		return fmt.Errorf("DB_MAX_CONNS must be >= 1")
	}
	if c.DBMinConns < 0 {
		return fmt.Errorf("DB_MIN_CONNS must be >= 0")
	}
	if c.DBMinConns > c.DBMaxConns {
		return fmt.Errorf("DB_MIN_CONNS cannot be greater than DB_MAX_CONNS")
	}
	if c.DBMaxConnLifetime <= 0 {
		return fmt.Errorf("DB_MAX_CONN_LIFETIME must be positive")
	}
	if c.DBMaxConnIdleTime <= 0 {
		return fmt.Errorf("DB_MAX_CONN_IDLE_TIME must be positive")
	}
	if c.DBConnectTimeout <= 0 {
		return fmt.Errorf("DB_CONNECT_TIMEOUT must be positive")
	}
	if c.DBHealthTimeout <= 0 {
		return fmt.Errorf("DB_HEALTH_TIMEOUT must be positive")
	}
	if c.PendingPollInterval <= 0 || c.PendingBaseDelay < time.Microsecond || c.PendingMaxDelay < c.PendingBaseDelay || c.PendingTTL < time.Microsecond || c.PendingMaxAttempts < 1 || c.PendingBatchSize < 1 {
		return fmt.Errorf("invalid PENDING_REFERENCE configuration: positive polling, attempts and batch required; base delay and TTL must be >= 1us; max delay must be >= base delay")
	}
	if err := c.validateSQS(); err != nil {
		return err
	}
	if err := c.validateOutbox(); err != nil {
		return err
	}
	if (c.SQSEnabled || c.OutboxEnabled) && c.AppEnv != "development" && c.AppEnv != "test" {
		if c.AWSAccessKeyID == "test" || c.AWSSecretAccessKey == "test" {
			return fmt.Errorf("dummy broker credentials require development/test")
		}
		brokerURLs := []string{c.SQSEndpoint}
		if c.SQSEnabled {
			brokerURLs = append(brokerURLs, c.SQSQueueURL)
		}
		if c.OutboxEnabled {
			brokerURLs = append(brokerURLs, c.EventQueueURL)
		}
		for _, raw := range brokerURLs {
			if raw != "" && !strings.HasPrefix(raw, "https://") {
				return fmt.Errorf("custom broker endpoint and queue URLs require HTTPS outside development/test")
			}
		}
	}
	if err := c.ValidateOIDC(); err != nil {
		return err
	}

	return nil
}

func (c Config) validateSQS() error {
	if !c.SQSEnabled {
		return nil
	}
	if strings.TrimSpace(c.AWSRegion) == "" || strings.TrimSpace(c.SQSConsumerName) == "" || !strings.HasSuffix(c.SQSQueueName, ".fifo") || !strings.HasSuffix(c.SQSDLQName, ".fifo") {
		return fmt.Errorf("SQS region, consumer name and FIFO queue names are required")
	}
	if (c.AWSAccessKeyID == "") != (c.AWSSecretAccessKey == "") {
		return fmt.Errorf("AWS access key and secret must be configured together")
	}
	for _, raw := range []string{c.SQSEndpoint, c.SQSQueueURL} {
		if raw != "" {
			u, err := url.Parse(raw)
			if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
				return fmt.Errorf("invalid SQS endpoint or queue URL")
			}
		}
	}
	if c.SQSWaitSeconds < 1 || c.SQSWaitSeconds > 20 || c.SQSMaxMessages < 1 || c.SQSMaxMessages > 10 || c.SQSConcurrency < 1 || c.SQSVisibilitySeconds < 1 || c.SQSVisibilitySeconds > 43200 || c.SQSProcessingTimeout <= 0 {
		return fmt.Errorf("invalid SQS polling, concurrency or visibility settings")
	}
	// One receive may contain an entire group; reserve time for its sequential tail and ACK.
	if c.SQSProcessingTimeout >= time.Duration(c.SQSVisibilitySeconds)*time.Second/time.Duration(c.SQSMaxMessages)-3*time.Second {
		return fmt.Errorf("SQS visibility must exceed batch processing plus acknowledgement budget")
	}
	return nil
}

// Addr returns the HTTP listen address.
func (c Config) Addr() string {
	return ":" + c.HTTPPort
}

func validateDatabaseURL(raw string) error {
	if strings.TrimSpace(raw) == "" {
		return fmt.Errorf("DATABASE_URL is required")
	}

	parsed, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("DATABASE_URL is invalid")
	}

	switch strings.ToLower(parsed.Scheme) {
	case "postgres", "postgresql":
	default:
		return fmt.Errorf("DATABASE_URL scheme must be postgres or postgresql")
	}

	if parsed.Host == "" {
		return fmt.Errorf("DATABASE_URL must include a host")
	}

	return nil
}

func getEnv(key, fallback string) string {
	if value, ok := os.LookupEnv(key); ok {
		return strings.TrimSpace(value)
	}
	return fallback
}

func durationEnv(key string, fallback time.Duration) (time.Duration, error) {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback, nil
	}

	d, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("invalid %s", key)
	}
	if d <= 0 {
		return 0, fmt.Errorf("%s must be positive", key)
	}
	return d, nil
}

func int32Env(key string, fallback int32) (int32, error) {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback, nil
	}

	value, err := strconv.ParseInt(raw, 10, 32)
	if err != nil {
		return 0, fmt.Errorf("invalid %s", key)
	}
	return int32(value), nil
}
