package httpadapter

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/junglegaming/backend-challenge-go/internal/application/financial"
	"github.com/junglegaming/backend-challenge-go/internal/domain/money"
	"github.com/junglegaming/backend-challenge-go/internal/domain/wallet"
	"github.com/junglegaming/backend-challenge-go/internal/identity"
	"github.com/junglegaming/backend-challenge-go/internal/ports"
)

type reconciliationSpy struct {
	calls     int
	err       error
	divergent bool
}

func (s *reconciliationSpy) Reconcile(_ context.Context, _ identity.Principal, id wallet.ID) (financial.ReconciliationResult, error) {
	s.calls++
	if s.err != nil {
		return financial.ReconciliationResult{}, s.err
	}
	version := int64(1)
	b := money.External{Amount: "100.00", Currency: "BRL"}
	r := financial.ReconciliationResult{WalletID: id, Currency: "BRL", Status: "CONSISTENT", WalletBalance: b, LedgerBalance: &b, WalletVersion: 1, ExpectedWalletVersion: &version, LedgerEntriesChecked: 1, TransactionsChecked: 1, Divergences: []financial.Divergence{}}
	if s.divergent {
		r.Status = "DIVERGENT"
		r.Divergences = append(r.Divergences, financial.Divergence{Code: financial.BalanceMismatch, Expected: "100.00", Actual: "99.00", Message: "wallet balance differs from ledger"})
	}
	return r, nil
}

func TestReconciliationHTTPResultsAuthorizationErrorsAndLogs(t *testing.T) {
	auth := identity.NewAuthorizer()
	spy := &reconciliationSpy{}
	logs := &bytes.Buffer{}
	h := NewHandler(nil, slog.New(slog.NewJSONHandler(logs, nil)), NewAuthMiddleware(authDouble{}, auth), &FinancialHandler{reconciliation: spy, authorizer: auth}, nil)
	for _, tc := range []struct {
		token  string
		status int
	}{{"", 401}, {"provider", 403}, {"internal", 200}} {
		w := callAPI(h, "POST", "/wallets/wallet/reconciliation", tc.token, "", "")
		if w.Code != tc.status {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	if spy.calls != 1 {
		t.Fatal("unauthorized audit reached application")
	}
	spy.divergent = true
	logs.Reset()
	w := callAPI(h, "POST", "/wallets/wallet/reconciliation", "internal", "", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"status":"DIVERGENT"`) {
		t.Fatal(w.Code, w.Body.String())
	}
	var event map[string]json.RawMessage
	if err := json.Unmarshal(bytes.TrimSpace(logs.Bytes()), &event); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"correlationId", "walletId", "reconciliationStatus", "ledgerEntriesChecked", "transactionsChecked", "divergenceCount", "durationMs"} {
		if len(event[key]) == 0 {
			t.Fatal("missing audit log field", key)
		}
	}
	if string(event["reconciliationStatus"]) != `"DIVERGENT"` || string(event["divergenceCount"]) != "1" || strings.Contains(logs.String(), "100.00") {
		t.Fatal("unsafe or incomplete audit log", logs.String())
	}
	for _, tc := range []struct {
		err    error
		status int
	}{{financial.ErrWalletNotFound, 404}, {ports.ErrPersistence, 503}, {context.Canceled, 503}, {context.DeadlineExceeded, 503}} {
		spy.err = tc.err
		logs.Reset()
		w = callAPI(h, "POST", "/wallets/wallet/reconciliation", "internal", "", "")
		if w.Code != tc.status || strings.Contains(w.Body.String(), `"status":"CONSISTENT"`) {
			t.Fatal(w.Code, w.Body.String())
		}
		if !strings.Contains(logs.String(), `"reconciliationStatus":"ERROR"`) {
			t.Fatal("technical failure not logged")
		}
	}
}
