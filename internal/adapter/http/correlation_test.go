package httpadapter

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/junglegaming/backend-challenge-go/internal/observability"
)

func TestCorrelationMiddlewareUsesIncomingHeader(t *testing.T) {
	var got string
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		value, ok := observability.CorrelationIDFromContext(r.Context())
		if !ok {
			t.Fatal("correlation ID missing from context")
		}
		got = value
		w.WriteHeader(http.StatusNoContent)
	})

	req := httptest.NewRequest(http.MethodGet, "/health/live", nil)
	req.Header.Set(correlationIDHeader, "corr-123")
	rec := httptest.NewRecorder()

	CorrelationMiddleware(next).ServeHTTP(rec, req)

	if got != "corr-123" {
		t.Fatalf("context correlation ID = %q, want corr-123", got)
	}
	if rec.Header().Get(correlationIDHeader) != "corr-123" {
		t.Fatalf("response header = %q, want corr-123", rec.Header().Get(correlationIDHeader))
	}
}

func TestCorrelationMiddlewareGeneratesIDWhenMissing(t *testing.T) {
	var got string
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		value, ok := observability.CorrelationIDFromContext(r.Context())
		if !ok {
			t.Fatal("correlation ID missing from context")
		}
		got = value
		w.WriteHeader(http.StatusNoContent)
	})

	req := httptest.NewRequest(http.MethodGet, "/health/live", nil)
	rec := httptest.NewRecorder()

	CorrelationMiddleware(next).ServeHTTP(rec, req)

	if got == "" {
		t.Fatal("expected generated correlation ID")
	}
	if rec.Header().Get(correlationIDHeader) != got {
		t.Fatalf("response header = %q, want %q", rec.Header().Get(correlationIDHeader), got)
	}
}
