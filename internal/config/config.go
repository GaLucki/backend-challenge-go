package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	defaultAppEnv          = "development"
	defaultHTTPPort        = "8080"
	defaultLogLevel        = "info"
	defaultShutdownTimeout = 10 * time.Second
)

// Config holds application configuration loaded from environment variables.
// Fields reserved for later phases are loaded when present but are not validated yet.
type Config struct {
	AppEnv          string
	HTTPPort        string
	LogLevel        string
	ShutdownTimeout time.Duration

	// Reserved for later phases (not connected in phase 0).
	DatabaseURL        string
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
		DatabaseURL:        os.Getenv("DATABASE_URL"),
		KeycloakURL:        os.Getenv("KEYCLOAK_URL"),
		SQSEndpoint:        os.Getenv("SQS_ENDPOINT"),
		AWSRegion:          os.Getenv("AWS_REGION"),
		AWSAccessKeyID:     os.Getenv("AWS_ACCESS_KEY_ID"),
		AWSSecretAccessKey: os.Getenv("AWS_SECRET_ACCESS_KEY"),
	}

	if raw := strings.TrimSpace(os.Getenv("SHUTDOWN_TIMEOUT")); raw != "" {
		d, err := time.ParseDuration(raw)
		if err != nil {
			return Config{}, fmt.Errorf("invalid SHUTDOWN_TIMEOUT: %w", err)
		}
		if d <= 0 {
			return Config{}, fmt.Errorf("SHUTDOWN_TIMEOUT must be positive")
		}
		cfg.ShutdownTimeout = d
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

	return nil
}

// Addr returns the HTTP listen address.
func (c Config) Addr() string {
	return ":" + c.HTTPPort
}

func getEnv(key, fallback string) string {
	if value, ok := os.LookupEnv(key); ok {
		return strings.TrimSpace(value)
	}
	return fallback
}
