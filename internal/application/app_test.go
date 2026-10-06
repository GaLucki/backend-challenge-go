package application_test

import (
	"testing"

	"github.com/junglegaming/backend-challenge-go/internal/application"
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
	)
	if err != nil {
		t.Fatalf("fx.ValidateApp() error = %v", err)
	}
}
