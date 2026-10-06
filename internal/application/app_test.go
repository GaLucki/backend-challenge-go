package application_test

import (
	"context"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/junglegaming/backend-challenge-go/internal/application"
	"github.com/junglegaming/backend-challenge-go/internal/application/financial"
	"github.com/junglegaming/backend-challenge-go/internal/ports"
	"go.uber.org/fx"
)

func TestFxAppConstructs(t *testing.T) {
	t.Setenv("APP_ENV", "test")
	t.Setenv("HTTP_PORT", "18080")
	t.Setenv("LOG_LEVEL", "info")
	t.Setenv("DATABASE_URL", "postgres://wagering:wagering@localhost:5432/wagering?sslmode=disable")

	err := fx.ValidateApp(
		fx.NopLogger,
		application.Module,
		fx.Invoke(func(ports.WalletRepository, ports.WagerTransactionRepository, ports.LedgerRepository, ports.InboxRepository, ports.OutboxRepository, ports.IdempotencyRepository, ports.ReversalRepository, ports.PendingReferenceRepository, ports.UnitOfWork, *financial.Service, *financial.PendingWorker) {
		}),
	)
	if err != nil {
		t.Fatalf("fx.ValidateApp() error = %v", err)
	}
}

func TestIntegrationFxLifecycle(t *testing.T) {
	t.Setenv("SQS_ENABLED", "false")
	t.Setenv("OUTBOX_PUBLISHER_ENABLED", "false")
	testFxLifecycle(t)
}

func TestIntegrationFxSQSLifecycle(t *testing.T) {
	endpoint := os.Getenv("TEST_SQS_ENDPOINT")
	if endpoint == "" {
		t.Skip("set TEST_SQS_ENDPOINT to test Fx lifecycle with real SQS")
	}
	t.Setenv("SQS_ENABLED", "true")
	t.Setenv("OUTBOX_PUBLISHER_ENABLED", "false")
	t.Setenv("SQS_ENDPOINT", endpoint)
	t.Setenv("AWS_REGION", "us-east-1")
	t.Setenv("AWS_ACCESS_KEY_ID", "test")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "test")
	t.Setenv("SQS_QUEUE_NAME", "wager-transactions.fifo")
	t.Setenv("SQS_QUEUE_URL", "")
	testFxLifecycle(t)
}

func TestIntegrationFxOutboxLifecycle(t *testing.T) {
	endpoint := os.Getenv("TEST_SQS_ENDPOINT")
	if endpoint == "" {
		t.Skip("set TEST_SQS_ENDPOINT for real Outbox lifecycle")
	}
	t.Setenv("TEST_DATABASE_URL", isolatedPublisherDatabase(t))
	t.Setenv("SQS_ENABLED", "true")
	t.Setenv("OUTBOX_PUBLISHER_ENABLED", "true")
	t.Setenv("SQS_ENDPOINT", endpoint)
	t.Setenv("AWS_REGION", "us-east-1")
	t.Setenv("AWS_ACCESS_KEY_ID", "test")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "test")
	t.Setenv("EVENT_QUEUE_NAME", "wager-events.fifo")
	t.Setenv("EVENT_QUEUE_URL", "")
	testFxLifecycle(t)
}

func TestIntegrationFxOutboxRejectsCommandQueueAlias(t *testing.T) {
	endpoint := os.Getenv("TEST_SQS_ENDPOINT")
	database := os.Getenv("TEST_DATABASE_URL")
	if endpoint == "" || database == "" {
		t.Skip("set TEST_DATABASE_URL and TEST_SQS_ENDPOINT for queue separation integration")
	}
	database = isolatedPublisherDatabase(t)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()
	t.Setenv("DATABASE_URL", database)
	t.Setenv("HTTP_PORT", strconv.Itoa(port))
	t.Setenv("LOG_LEVEL", "error")
	t.Setenv("SQS_ENABLED", "false")
	t.Setenv("SQS_QUEUE_URL", "")
	t.Setenv("OUTBOX_PUBLISHER_ENABLED", "true")
	t.Setenv("SQS_ENDPOINT", endpoint)
	t.Setenv("AWS_REGION", "us-east-1")
	t.Setenv("AWS_ACCESS_KEY_ID", "test")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "test")
	t.Setenv("EVENT_QUEUE_NAME", "wager-events.fifo")
	t.Setenv("EVENT_QUEUE_URL", strings.TrimRight(endpoint, "/")+"/queue/us-east-1/000000000000/wager-transactions.fifo")
	app := fx.New(fx.NopLogger, application.Module)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	err = app.Start(ctx)
	if err == nil {
		_ = app.Stop(ctx)
		t.Fatal("publisher accepted input queue through URL alias")
	}
	if !strings.Contains(err.Error(), "cannot publish into a command queue") {
		t.Fatal("startup failed for a different reason", err)
	}
}

func testFxLifecycle(t *testing.T) {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("set TEST_DATABASE_URL to test lifecycle with real PostgreSQL")
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DATABASE_URL", url)
	t.Setenv("HTTP_PORT", strconv.Itoa(port))
	t.Setenv("APP_ENV", "test")
	t.Setenv("LOG_LEVEL", "error")
	app := fx.New(fx.NopLogger, application.Module)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := app.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer stopCancel()
		if err := app.Stop(stopCtx); err != nil {
			t.Errorf("graceful shutdown: %v", err)
		}
	}()
	client := &http.Client{Timeout: 3 * time.Second}
	for _, path := range []string{"/health/live", "/health/ready"} {
		resp, err := client.Get("http://127.0.0.1:" + strconv.Itoa(port) + path)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s status=%d", path, resp.StatusCode)
		}
	}
}
