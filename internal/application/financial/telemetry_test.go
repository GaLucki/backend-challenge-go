package financial

import (
	"context"
	"errors"
	"testing"

	"github.com/junglegaming/backend-challenge-go/internal/config"
	"github.com/junglegaming/backend-challenge-go/internal/domain/wager"
	"github.com/junglegaming/backend-challenge-go/internal/domain/wallet"
	"github.com/junglegaming/backend-challenge-go/internal/identity"
	"github.com/junglegaming/backend-challenge-go/internal/observability"
	"github.com/junglegaming/backend-challenge-go/internal/ports"
)

func financialCounter(t *testing.T, m *observability.Metrics, name string, labels map[string]string) float64 {
	t.Helper()
	families, err := m.Registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range families {
		if f.GetName() != name {
			continue
		}
		for _, metric := range f.Metric {
			matches := true
			for _, label := range metric.Label {
				if labels[label.GetName()] != label.GetValue() {
					matches = false
				}
			}
			if matches {
				return metric.GetCounter().GetValue()
			}
		}
	}
	return 0
}

func TestReconciliationMetricsClassifyCompleteReportsAndTechnicalErrors(t *testing.T) {
	m := observability.NewMetrics(config.Config{MetricsEnabled: true})
	reader := &auditReaderSpy{snapshot: auditFixture()}
	service := NewReconciliationService(reader, identity.NewAuthorizer()).WithTelemetry(m)
	p := identity.Principal{Subject: "s", ClientID: "internal", Internal: true, Roles: []string{identity.RoleInternal}}
	if _, err := service.Reconcile(context.Background(), p, "wallet"); err != nil {
		t.Fatal(err)
	}
	money := reader.snapshot.Wallet.Balance()
	reader.snapshot.Wallet, _ = wallet.Rehydrate("wallet", "player", "BRL", money, 9)
	if r, err := service.Reconcile(context.Background(), p, "wallet"); err != nil || r.Status != "DIVERGENT" {
		t.Fatal(r, err)
	}
	reader.err = ports.ErrPersistence
	if _, err := service.Reconcile(context.Background(), p, "wallet"); err == nil {
		t.Fatal("technical failure lost")
	}
	for _, result := range []string{"CONSISTENT", "DIVERGENT", "ERROR"} {
		if financialCounter(t, m, "wagering_reconciliation_runs_total", map[string]string{"result": result}) != 1 {
			t.Fatal("audit outcome missing", result)
		}
	}
	if financialCounter(t, m, "wagering_reconciliation_divergences_total", map[string]string{"code": "WALLET_VERSION_MISMATCH"}) != 1 {
		t.Fatal("divergence not counted")
	}
}

func TestFinancialMetricsOnlyCountDurableEffectsAndSeparateReplay(t *testing.T) {
	ctx := context.Background()
	s, u := unitService()
	m := observability.NewMetrics(config.Config{MetricsEnabled: true})
	s.WithTelemetry(m, nil)
	w := createUnitWallet(t, s, 10000)
	in := input(t, w.WalletID, wager.TypeBet, 2000)
	if _, err := s.ProcessHTTPWager(ctx, in); err != nil {
		t.Fatal(err)
	}
	if r, err := s.ProcessHTTPWager(ctx, in); err != nil || !r.IdempotentReplay {
		t.Fatal(r, err)
	}
	check := func(name string, labels map[string]string, want float64) {
		t.Helper()
		if got := financialCounter(t, m, "wagering_"+name, labels); got != want {
			t.Fatal(name, labels, got, want)
		}
	}
	check("financial_movements_total", map[string]string{"operation": "BET"}, 1)
	check("financial_operations_total", map[string]string{"operation": "BET", "state": "PROCESSED"}, 1)
	check("idempotency_events_total", map[string]string{"outcome": "replay"}, 1)
	in.Amount = cents(t, 2100, "BRL")
	if _, err := s.ProcessHTTPWager(ctx, in); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatal(err)
	}
	check("idempotency_events_total", map[string]string{"outcome": "idempotency_conflict"}, 1)
	in.ExternalTransactionID = "rollback-test"
	in.IdempotencyKey = "rollback-test"
	u.failOutbox = true
	if _, err := s.ProcessHTTPWager(ctx, in); err == nil {
		t.Fatal("expected rollback")
	}
	check("financial_movements_total", map[string]string{"operation": "BET"}, 1)
	check("financial_operations_total", map[string]string{"operation": "BET", "state": "PROCESSED"}, 1)
	u.failOutbox = false
	in.ExternalTransactionID = "rejected"
	in.IdempotencyKey = "rejected"
	in.Amount = cents(t, 100000, "BRL")
	if r, err := s.ProcessHTTPWager(ctx, in); err != nil || r.State != wager.StateRejected {
		t.Fatal(r, err)
	}
	check("financial_operations_total", map[string]string{"operation": "BET", "state": "REJECTED"}, 1)
	check("financial_movements_total", map[string]string{"operation": "BET"}, 1)
}
