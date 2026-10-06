package httpadapter

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/junglegaming/backend-challenge-go/internal/config"
)

type stubPinger struct {
	err error
}

func (s stubPinger) Ping(context.Context) error {
	return s.err
}

func TestLiveEndpoint(t *testing.T) {
	handler := NewHealthHandler(&atomic.Bool{}, stubPinger{}, config.Config{DBHealthTimeout: time.Second})
	req := httptest.NewRequest(http.MethodGet, "/health/live", nil)
	rec := httptest.NewRecorder()

	handler.Live(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	var body healthResponse
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Status != "ok" {
		t.Fatalf("status = %q, want ok", body.Status)
	}
}

func TestLiveEndpointDoesNotDependOnDatabase(t *testing.T) {
	handler := NewHealthHandler(&atomic.Bool{}, stubPinger{err: errors.New("down")}, config.Config{DBHealthTimeout: time.Second})
	req := httptest.NewRequest(http.MethodGet, "/health/live", nil)
	rec := httptest.NewRecorder()

	handler.Live(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
}

func TestReadyEndpointWhenDatabaseAvailable(t *testing.T) {
	ready := &atomic.Bool{}
	ready.Store(true)
	handler := NewHealthHandler(ready, stubPinger{}, config.Config{DBHealthTimeout: time.Second})

	req := httptest.NewRequest(http.MethodGet, "/health/ready", nil)
	rec := httptest.NewRecorder()

	handler.Ready(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	var body healthResponse
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Status != "ok" {
		t.Fatalf("status = %q, want ok", body.Status)
	}
}

func TestReadyEndpointWhenDatabaseUnavailable(t *testing.T) {
	ready := &atomic.Bool{}
	ready.Store(true)
	handler := NewHealthHandler(ready, stubPinger{err: errors.New("connection refused")}, config.Config{DBHealthTimeout: time.Second})

	req := httptest.NewRequest(http.MethodGet, "/health/ready", nil)
	rec := httptest.NewRecorder()

	handler.Ready(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusServiceUnavailable)
	}

	var body errorResponse
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Error.Code != ErrorCodeNotReady {
		t.Fatalf("code = %q, want %q", body.Error.Code, ErrorCodeNotReady)
	}
	if body.Error.Message != "database unavailable" {
		t.Fatalf("message = %q, want database unavailable", body.Error.Message)
	}
}

func TestReadyEndpointWhenNotInitialized(t *testing.T) {
	handler := NewHealthHandler(&atomic.Bool{}, stubPinger{}, config.Config{DBHealthTimeout: time.Second})

	req := httptest.NewRequest(http.MethodGet, "/health/ready", nil)
	rec := httptest.NewRecorder()

	handler.Ready(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusServiceUnavailable)
	}

	var body errorResponse
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Error.Code != ErrorCodeNotReady {
		t.Fatalf("code = %q, want %q", body.Error.Code, ErrorCodeNotReady)
	}
}
