package config

import "testing"

func TestBrokerLocalCredentialsCannotEscapeDevelopment(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://user:pass@localhost:5432/db")
	t.Setenv("APP_ENV", "development")
	t.Setenv("SQS_ENABLED", "true")
	t.Setenv("AWS_ACCESS_KEY_ID", "test")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "test")
	t.Setenv("SQS_ENDPOINT", "http://localhost:4566")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	cfg.AppEnv = "production"
	if cfg.Validate() == nil {
		t.Fatal("local dummy credentials accepted in production")
	}
	cfg.AWSAccessKeyID, cfg.AWSSecretAccessKey = "", "" // SDK workload identity chain
	if cfg.Validate() == nil {
		t.Fatal("insecure custom endpoint accepted in production")
	}
	cfg.SQSEndpoint = ""
	cfg.SQSQueueURL = "http://localhost:4566/commands.fifo"
	if cfg.Validate() == nil {
		t.Fatal("insecure explicit command queue URL accepted")
	}
	cfg.SQSQueueURL = ""
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
}
