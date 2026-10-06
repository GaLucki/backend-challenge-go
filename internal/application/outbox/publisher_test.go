package outbox

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"math"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/junglegaming/backend-challenge-go/internal/ports"
)

type testStore struct {
	claim   func(context.Context, string, string, int, time.Duration) ([]ports.OutboxClaim, error)
	renew   func(context.Context, ports.OutboxClaim, time.Duration) (bool, error)
	mark    func(context.Context, ports.OutboxClaim) (bool, error)
	retry   func(context.Context, ports.OutboxClaim, time.Duration) (bool, error)
	release func(context.Context, ports.OutboxClaim) (bool, error)
}

func (s testStore) Claim(ctx context.Context, o, t string, n int, d time.Duration) ([]ports.OutboxClaim, error) {
	return s.claim(ctx, o, t, n, d)
}
func (s testStore) Renew(ctx context.Context, c ports.OutboxClaim, d time.Duration) (bool, error) {
	if s.renew != nil {
		return s.renew(ctx, c, d)
	}
	return true, nil
}
func (s testStore) Published(ctx context.Context, c ports.OutboxClaim) (bool, error) {
	return s.mark(ctx, c)
}
func (s testStore) Retry(ctx context.Context, c ports.OutboxClaim, d time.Duration) (bool, error) {
	return s.retry(ctx, c, d)
}
func (s testStore) Release(ctx context.Context, c ports.OutboxClaim) (bool, error) {
	return s.release(ctx, c)
}

type sendFunc func(context.Context, ports.OutboxEvent) error

func (f sendFunc) Send(ctx context.Context, e ports.OutboxEvent) error { return f(ctx, e) }
func testOptions() Options {
	return Options{PublisherID: "publisher", PollInterval: time.Second, BatchSize: 10, BaseRetryDelay: time.Second, MaxRetryDelay: time.Minute, ClaimDuration: 30 * time.Second, PublishTimeout: 5 * time.Second, StoreTimeout: 3 * time.Second}
}
func testClaim() ports.OutboxClaim {
	return ports.OutboxClaim{Event: ports.OutboxEvent{EventID: "event", AggregateID: "wallet", EventType: "WalletBalanceChanged", RetryCount: 2, Payload: []byte(`{"correlationId":"correlation","secret":"must-not-log","money":{"amount":"25.00"}}`)}, Token: "token", ClaimedBy: "publisher"}
}
func TestBackoffSaturates(t *testing.T) {
	for _, tc := range []struct {
		attempt int32
		want    time.Duration
	}{{1, time.Second}, {2, 2 * time.Second}, {3, 4 * time.Second}, {6, 32 * time.Second}, {7, time.Minute}, {math.MaxInt32, time.Minute}} {
		if got := Backoff(time.Second, time.Minute, tc.attempt); got != tc.want {
			t.Fatal(tc, got)
		}
	}
	if Backoff(time.Duration(math.MaxInt64/2+1), time.Duration(math.MaxInt64), 2) != time.Duration(math.MaxInt64) {
		t.Fatal("backoff overflow")
	}
}
func TestPublishClaimSuccessFailureAndFencing(t *testing.T) {
	sentinel := errors.New("broker error with sensitive detail")
	for _, mode := range []string{"success", "send-failure", "mark-failure", "expired-lease", "stale-mark"} {
		t.Run(mode, func(t *testing.T) {
			var sent, marked, retried bool
			var logs bytes.Buffer
			c := testClaim()
			store := testStore{
				renew: func(context.Context, ports.OutboxClaim, time.Duration) (bool, error) {
					return mode != "expired-lease", nil
				},
				mark: func(_ context.Context, got ports.OutboxClaim) (bool, error) {
					if !sent {
						t.Error("mark before send")
					}
					if got.Event.EventID != c.Event.EventID || got.Token != c.Token {
						t.Error("identity changed")
					}
					marked = true
					if mode == "mark-failure" {
						return false, ports.ErrPersistence
					}
					return mode != "stale-mark", nil
				},
				retry: func(_ context.Context, got ports.OutboxClaim, d time.Duration) (bool, error) {
					retried = true
					if got.Event.RetryCount != 2 || d != 4*time.Second {
						t.Error("retry history/backoff changed")
					}
					return true, nil
				},
			}
			p, err := NewPublisher(store, sendFunc(func(_ context.Context, e ports.OutboxEvent) error {
				sent = true
				if string(e.Payload) != string(c.Event.Payload) {
					t.Error("envelope reconstructed")
				}
				if mode == "send-failure" {
					return sentinel
				}
				return nil
			}), testOptions(), slog.New(slog.NewJSONHandler(&logs, nil)))
			if err != nil {
				t.Fatal(err)
			}
			err = p.PublishClaim(context.Background(), c)
			if mode == "success" && err != nil {
				t.Fatal(err)
			}
			if mode != "success" && err == nil {
				t.Fatal("failure hidden")
			}
			if mode == "expired-lease" && (sent || marked || retried) {
				t.Fatal("expired owner sent")
			}
			if (mode == "send-failure") != retried {
				t.Fatal("incorrect failed attempt persistence")
			}
			if mode == "send-failure" && marked {
				t.Fatal("failed send marked published")
			}
			if strings.Contains(logs.String(), "must-not-log") || strings.Contains(logs.String(), "25.00") || strings.Contains(logs.String(), "sensitive") {
				t.Fatal("payload/error leaked in logs")
			}
		})
	}
}
func TestRunStopsPollingAndDrainsActiveSend(t *testing.T) {
	started, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var calls, marks atomic.Int32
	store := testStore{claim: func(context.Context, string, string, int, time.Duration) ([]ports.OutboxClaim, error) {
		calls.Add(1)
		return []ports.OutboxClaim{testClaim()}, nil
	}, mark: func(context.Context, ports.OutboxClaim) (bool, error) { marks.Add(1); return true, nil }}
	p, err := NewPublisher(store, sendFunc(func(ctx context.Context, _ ports.OutboxEvent) error {
		close(started)
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}), testOptions(), slog.New(slog.NewJSONHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	poll, stop := context.WithCancel(context.Background())
	defer stop()
	go func() { defer close(done); p.Run(poll, context.Background()) }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("send did not start")
	}
	stop()
	select {
	case <-done:
		t.Fatal("active send did not drain")
	default:
	}
	close(release)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("publisher orphaned")
	}
	if calls.Load() != 1 || marks.Load() != 1 {
		t.Fatal("polling continued after stop")
	}
}
func TestRunOnceUsesClaimedBatchAndCanceledPolling(t *testing.T) {
	store := testStore{claim: func(_ context.Context, owner, token string, limit int, d time.Duration) ([]ports.OutboxClaim, error) {
		if owner != "publisher" || token == "" || limit != 10 || d != 30*time.Second {
			t.Error("claim arguments")
		}
		return nil, nil
	}}
	p, err := NewPublisher(store, sendFunc(func(context.Context, ports.OutboxEvent) error { t.Error("noneligible event sent"); return nil }), testOptions(), slog.New(slog.NewJSONHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	if n, err := p.RunOnce(context.Background(), context.Background()); n != 0 || err != nil {
		t.Fatal(n, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := p.RunOnce(ctx, context.Background()); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestCanceledBatchReleasesAllUnsentClaimsInOneTimeout(t *testing.T) {
	poll, cancel := context.WithCancel(context.Background())
	defer cancel()
	var calls, releases atomic.Int32
	store := testStore{
		claim: func(context.Context, string, string, int, time.Duration) ([]ports.OutboxClaim, error) {
			claims := make([]ports.OutboxClaim, 10)
			for i := range claims {
				claims[i] = testClaim()
			}
			cancel()
			return claims, nil
		},
		release: func(ctx context.Context, _ ports.OutboxClaim) (bool, error) {
			calls.Add(1)
			<-ctx.Done()
			releases.Add(1)
			return false, ctx.Err()
		},
	}
	opts := testOptions()
	opts.StoreTimeout = 50 * time.Millisecond
	p, err := NewPublisher(store, sendFunc(func(context.Context, ports.OutboxEvent) error { t.Error("unsent canceled work published"); return nil }), opts, slog.New(slog.NewJSONHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	_, err = p.RunOnce(poll, context.Background())
	if err == nil || calls.Load() != 10 || releases.Load() != 10 {
		t.Fatal("claims not cleaned up", err, calls.Load(), releases.Load())
	}
	if time.Since(start) > 350*time.Millisecond {
		t.Fatal("cleanup serialized timeouts")
	}
}
