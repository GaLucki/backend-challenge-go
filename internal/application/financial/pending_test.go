package financial

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"log/slog"
	"math"
	"testing"
	"time"
)

func TestPendingPolicyBackoffAndValidation(t *testing.T) {
	policy := DefaultPendingPolicy()
	for _, tc := range []struct {
		attempt int32
		want    time.Duration
	}{{1, time.Second}, {2, 2 * time.Second}, {3, 4 * time.Second}, {7, time.Minute}, {100, time.Minute}} {
		if got := policy.backoff(tc.attempt); got != tc.want {
			t.Fatalf("attempt=%d got=%s want=%s", tc.attempt, got, tc.want)
		}
	}
	policy.BaseDelay = time.Duration(math.MaxInt64 / 2)
	policy.MaxDelay = time.Duration(math.MaxInt64)
	if policy.backoff(3) != policy.MaxDelay {
		t.Fatal("backoff overflow")
	}
	for _, modify := range []func(*PendingPolicy){func(p *PendingPolicy) { p.BaseDelay = 0 }, func(p *PendingPolicy) { p.MaxDelay = 0 }, func(p *PendingPolicy) { p.MaxAttempts = 0 }, func(p *PendingPolicy) { p.TTL = 0 }} {
		p := DefaultPendingPolicy()
		modify(&p)
		if p.Validate() == nil {
			t.Fatal("invalid policy accepted")
		}
	}
}
func TestWorkerBatchAndCancellation(t *testing.T) {
	s, u := unitService()
	w := createUnitWallet(t, s, 10000)
	for _, name := range []string{"a", "b"} {
		runUnitWager(t, s, reversalInput(t, w.WalletID, "REFUND", 2000, name, "missing"))
	}
	at := s.now().Add(time.Second)
	s.now = func() time.Time { return at }
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	worker, err := NewPendingWorker(s, WorkerOptions{PollInterval: time.Hour, BatchSize: 1}, logger)
	if err != nil {
		t.Fatal(err)
	}
	if n, err := worker.RunOnce(context.Background()); err != nil || n != 1 {
		t.Fatalf("count=%d error=%v", n, err)
	}
	if n, err := worker.RunOnce(context.Background()); err != nil || n != 1 {
		t.Fatalf("count=%d error=%v", n, err)
	}
	if n, err := worker.RunOnce(context.Background()); err != nil || n != 0 {
		t.Fatalf("count=%d error=%v", n, err)
	}
	for _, p := range u.state.pending {
		if p.AttemptCount != 1 {
			t.Fatal(p)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); worker.Run(ctx) }()
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("worker did not stop")
	}
	if n, err := worker.RunOnce(ctx); err != context.Canceled || n != 0 {
		t.Fatal("canceled batch ran")
	}
	if _, err := NewPendingWorker(s, WorkerOptions{PollInterval: 0, BatchSize: 1}, logger); err == nil {
		t.Fatal("invalid interval")
	}
	if _, err := NewPendingWorker(s, WorkerOptions{PollInterval: time.Second, BatchSize: 0}, logger); err == nil {
		t.Fatal("invalid batch")
	}
}
func TestHashPreservesPhase4AndIncludesReference(t *testing.T) {
	in := input(t, "wallet", "BET", 2000).WagerInput
	legacy := `{"version":1,"providerId":"provider","externalTransactionId":"external","playerId":"player","walletId":"wallet","type":"BET","amountCents":2000,"currency":"BRL","roundId":"round"}`
	digest := sha256.Sum256([]byte(legacy))
	if CanonicalPayloadHash(in) != hex.EncodeToString(digest[:]) {
		t.Fatal("existing idempotency hashes changed")
	}
	in.Type = "REFUND"
	in.ReferenceExternalTransactionID = "reference-a"
	hash := CanonicalPayloadHash(in)
	in.ReferenceExternalTransactionID = "reference-b"
	if hash == CanonicalPayloadHash(in) {
		t.Fatal("reference excluded from hash")
	}
}
