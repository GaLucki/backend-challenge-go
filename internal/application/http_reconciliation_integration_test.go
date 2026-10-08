package application_test

import (
	"encoding/json"
	"testing"

	"github.com/junglegaming/backend-challenge-go/internal/application/financial"
)

func TestIntegrationReconciliationHTTPRealKeycloak(t *testing.T) {
	f := newAPIFixture(t)
	w := f.wallet(t, "phase10-http", "100.00")
	path := "/wallets/" + string(w.WalletID) + "/reconciliation"
	for _, tc := range []struct {
		identity string
		status   int
	}{{"", 401}, {"provider-a", 403}, {"provider-b", 403}} {
		f.request(t, "POST", path, tc.identity, nil, "", tc.status)
	}
	raw := f.request(t, "POST", path, "internal-service", nil, "", 200)
	var r financial.ReconciliationResult
	if err := json.Unmarshal(raw, &r); err != nil || r.Status != "CONSISTENT" || r.LedgerEntriesChecked != 1 || r.TransactionsChecked != 1 || r.LedgerBalance == nil || r.LedgerBalance.Amount != "100.00" {
		t.Fatal("opening HTTP reconciliation", err, string(raw))
	}
	f.request(t, "POST", "/wallets/missing/reconciliation", "internal-service", nil, "", 404)
	zero := f.wallet(t, "phase10-zero", "0.00")
	raw = f.request(t, "POST", "/wallets/"+string(zero.WalletID)+"/reconciliation", "internal-service", nil, "", 200)
	if err := json.Unmarshal(raw, &r); err != nil || r.Status != "CONSISTENT" || r.LedgerEntriesChecked != 0 || *r.ExpectedWalletVersion != 1 {
		t.Fatal("zero HTTP audit", err, string(raw))
	}
	// Replay and audit require different semantics: audit has no key, claim,
	// transaction, ledger, inbox or outbox writes.
	f.wager(t, "provider-a", wagerPayload(string(w.WalletID), "phase10-http", "bet", "BET", "20.00", ""), "bet", 201)
	_, err := f.pool.Exec(f.ctx, `UPDATE wallets SET balance_cents=7900 WHERE id=$1`, string(w.WalletID))
	if err != nil {
		t.Fatal(err)
	}
	var before, after string
	evidence := `SELECT jsonb_build_object('wallets',(SELECT jsonb_agg(to_jsonb(t) ORDER BY id) FROM wallets t),'transactions',(SELECT jsonb_agg(to_jsonb(t) ORDER BY transaction_id) FROM wager_transactions t),'ledger',(SELECT jsonb_agg(to_jsonb(t) ORDER BY id) FROM wallet_ledger_entries t),'outbox',(SELECT jsonb_agg(to_jsonb(t) ORDER BY event_id) FROM outbox_events t),'inbox',(SELECT coalesce(jsonb_agg(to_jsonb(t)),'[]') FROM inbox_messages t),'idempotency',(SELECT jsonb_agg(to_jsonb(t) ORDER BY idempotency_key) FROM wager_idempotency_records t))::text`
	if err = f.pool.QueryRow(f.ctx, evidence).Scan(&before); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		raw = f.request(t, "POST", path, "internal-service", nil, "", 200)
		if err = json.Unmarshal(raw, &r); err != nil || r.Status != "DIVERGENT" || r.WalletBalance.Amount != "79.00" || r.LedgerBalance.Amount != "80.00" {
			t.Fatal("HTTP divergent status", err, string(raw))
		}
		found := false
		for _, d := range r.Divergences {
			if d.Code == financial.BalanceMismatch {
				found = true
			}
		}
		if !found {
			t.Fatal("balance divergence absent")
		}
	}
	if err = f.pool.QueryRow(f.ctx, evidence).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Fatal("HTTP reconciliation changed evidence")
	}
	// Technical failure must not masquerade as a financial conclusion.
	_, err = f.pool.Exec(f.ctx, `ALTER TABLE wallet_ledger_entries RENAME TO unavailable_ledger`)
	if err != nil {
		t.Fatal(err)
	}
	f.request(t, "POST", path, "internal-service", nil, "", 503)
	_, err = f.pool.Exec(f.ctx, `ALTER TABLE unavailable_ledger RENAME TO wallet_ledger_entries`)
	if err != nil {
		t.Fatal(err)
	}
}
