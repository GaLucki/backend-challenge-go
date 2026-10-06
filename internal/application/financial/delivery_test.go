package financial

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/junglegaming/backend-challenge-go/internal/ports"
)

type inboxDouble struct{ state *memoryState }

func (r inboxDouble) Create(_ context.Context, m ports.InboxMessage) error {
	key := [2]string{m.ConsumerName, m.MessageID}
	if _, ok := r.state.inbox[key]; ok {
		return ports.ErrUniqueViolation
	}
	r.state.inbox[key] = m
	return nil
}
func (r inboxDouble) Claim(ctx context.Context, m ports.InboxMessage) (bool, error) {
	err := r.Create(ctx, m)
	if errors.Is(err, ports.ErrUniqueViolation) {
		return false, nil
	}
	return err == nil, err
}
func (r inboxDouble) Get(_ context.Context, consumer, id string) (ports.InboxMessage, error) {
	m, ok := r.state.inbox[[2]string{consumer, id}]
	if !ok {
		return m, ports.ErrNotFound
	}
	return m, nil
}
func (r inboxDouble) GetForUpdate(ctx context.Context, consumer, id string) (ports.InboxMessage, error) {
	return r.Get(ctx, consumer, id)
}
func (r inboxDouble) Complete(_ context.Context, consumer, id string, at time.Time) error {
	key := [2]string{consumer, id}
	m, ok := r.state.inbox[key]
	if !ok {
		return ports.ErrNotFound
	}
	m.CompletedAt = &at
	r.state.inbox[key] = m
	return nil
}
func TestDeliveryInboxAndRecovery(t *testing.T) {
	for _, incomplete := range []bool{false, true} {
		s, u := unitService()
		w := createUnitWallet(t, s, 10000)
		in := DeliveryInput{ConsumerName: "consumer", MessageID: "message", PayloadHash: strings.Repeat("a", 64), Wager: input(t, w.WalletID, "BET", 2000).WagerInput}
		if incomplete {
			u.state.inbox[[2]string{"consumer", "message"}] = ports.InboxMessage{ConsumerName: "consumer", MessageID: "message", PayloadHash: in.PayloadHash, ReceivedAt: s.now()}
		}
		result, err := s.ProcessDelivery(context.Background(), in)
		if err != nil || result.Outcome != "PROCESSED" {
			t.Fatalf("result=%+v err=%v", result, err)
		}
		if u.state.inbox[[2]string{"consumer", "message"}].CompletedAt == nil || u.state.wallets[w.WalletID].Balance().Cents() != 8000 {
			t.Fatal("inbox not completed with finance")
		}
		before := u.state
		replay, err := NewService(u).ProcessDelivery(context.Background(), in)
		if err != nil || replay.Outcome != "duplicate" || !reflect.DeepEqual(before, u.state) {
			t.Fatal("duplicate applied effects")
		}
		in.PayloadHash = strings.Repeat("b", 64)
		_, err = s.ProcessDelivery(context.Background(), in)
		if !errors.Is(err, ErrDeliveryIntegrity) || !reflect.DeepEqual(before, u.state) {
			t.Fatal("integrity conflict changed state")
		}
	}
}
func TestDeliveryBusinessRejectionAndPendingAreCompleted(t *testing.T) {
	for _, pending := range []bool{false, true} {
		s, u := unitService()
		w := createUnitWallet(t, s, 10000)
		wagerIn := input(t, w.WalletID, "BET", 20000).WagerInput
		want := "REJECTED"
		if pending {
			wagerIn.Type = "REFUND"
			wagerIn.Amount = cents(t, 2000, "BRL")
			wagerIn.ReferenceExternalTransactionID = "missing"
			want = "PENDING_REFERENCE"
		}
		result, err := s.ProcessDelivery(context.Background(), DeliveryInput{ConsumerName: "consumer", MessageID: "message", PayloadHash: strings.Repeat("a", 64), Wager: wagerIn})
		if err != nil || result.Outcome != want || u.state.inbox[[2]string{"consumer", "message"}].CompletedAt == nil || u.state.wallets[w.WalletID].Balance().Cents() != 10000 {
			t.Fatalf("result=%+v error=%v", result, err)
		}
	}
}
func TestDeliveryRollbackAndCrossTransport(t *testing.T) {
	s, u := unitService()
	w := createUnitWallet(t, s, 10000)
	in := DeliveryInput{ConsumerName: "consumer", MessageID: "message", PayloadHash: strings.Repeat("a", 64), Wager: input(t, w.WalletID, "BET", 2000).WagerInput}
	snapshot := u.state
	u.failOutbox = true
	_, err := s.ProcessDelivery(context.Background(), in)
	if !errors.Is(err, ErrPersistence) || !reflect.DeepEqual(snapshot, u.state) {
		t.Fatal("partial inbox/finance committed")
	}
	u.failOutbox = false
	http := input(t, w.WalletID, "BET", 2000)
	runUnitWager(t, s, http)
	result, err := s.ProcessDelivery(context.Background(), in)
	if err != nil || result.Outcome != "external_duplicate" || u.state.wallets[w.WalletID].Balance().Cents() != 8000 {
		t.Fatal(result, err)
	}
	conflict := in
	conflict.MessageID = "other-message"
	conflict.Wager.Amount = cents(t, 3000, "BRL")
	before := u.state
	_, err = s.ProcessDelivery(context.Background(), conflict)
	if !errors.Is(err, ErrExternalPayloadConflict) || !reflect.DeepEqual(before, u.state) {
		t.Fatal("external conflict applied")
	}
	s, u = unitService()
	w = createUnitWallet(t, s, 10000)
	http = input(t, w.WalletID, "BET", 2000)
	in.Wager = http.WagerInput
	_, err = s.ProcessDelivery(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.ProcessHTTPWager(context.Background(), http)
	if !errors.Is(err, ErrDuplicateExternalTransaction) || u.state.wallets[w.WalletID].Balance().Cents() != 8000 {
		t.Fatal("SQS -> HTTP reapplied")
	}
}
