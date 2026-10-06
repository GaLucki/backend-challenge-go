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
// Fields reserved for later phases are loaded when present but are not validated yet.
type Config struct {
	AppEnv          string
	HTTPPort        string
	LogLevel        string
	ShutdownTimeout time.Duration

	DatabaseURL       string
	DBMaxConns        int32
	DBMinConns        int32
	DBMaxConnLifetime time.Duration
	DBMaxConnIdleTime time.Duration
	DBConnectTimeout  time.Duration
	DBHealthTimeout   time.Duration

	// Reserved for later phases (not connected in phase 1).
	KeycloakURL        string
	SQSEndpoint        string
	AWSRegion          string
	AWSAccessKeyID     string
	AWSSecretAccessKey string
}

// Load reads configuration from environment variables and validates required fields.
func Load() (Config, error) {
	cfg := Config{
		AppEnv:             getEnv("APP_ENV", defaultAppEnv),
		HTTPPort:           getEnv("HTTP_PORT", defaultHTTPPort),
		LogLevel:           strings.ToLower(getEnv("LOG_LEVEL", defaultLogLevel)),
		ShutdownTimeout:    defaultShutdownTimeout,
		DatabaseURL:        strings.TrimSpace(os.Getenv("DATABASE_URL")),
		DBMaxConns:         defaultDBMaxConns,
		DBMinConns:         defaultDBMinConns,
		DBMaxConnLifetime:  defaultDBMaxConnLifetime,
		DBMaxConnIdleTime:  defaultDBMaxConnIdleTime,
		DBConnectTimeout:   defaultDBConnectTimeout,
		DBHealthTimeout:    defaultDBHealthTimeout,
		KeycloakURL:        os.Getenv("KEYCLOAK_URL"),
		SQSEndpoint:        os.Getenv("SQS_ENDPOINT"),
		AWSRegion:          os.Getenv("AWS_REGION"),
		AWSAccessKeyID:     os.Getenv("AWS_ACCESS_KEY_ID"),
		AWSSecretAccessKey: os.Getenv("AWS_SECRET_ACCESS_KEY"),
	}

	var err error

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

	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}

	return cfg, nil
}

// Validate checks required configuration values.
func (c Config) Validate() error {
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
		return fmt.Errorf("DATABASE_URL is invalid: %w", err)
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
		return 0, fmt.Errorf("invalid %s: %w", key, err)
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
		return 0, fmt.Errorf("invalid %s: %w", key, err)
	}
	return int32(value), nil
}
