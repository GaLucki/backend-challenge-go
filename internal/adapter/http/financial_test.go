package httpadapter

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/junglegaming/backend-challenge-go/internal/application/financial"
	"github.com/junglegaming/backend-challenge-go/internal/config"
	"github.com/junglegaming/backend-challenge-go/internal/domain/money"
	"github.com/junglegaming/backend-challenge-go/internal/domain/wager"
	"github.com/junglegaming/backend-challenge-go/internal/domain/wallet"
	"github.com/junglegaming/backend-challenge-go/internal/identity"
	"github.com/junglegaming/backend-challenge-go/internal/ports"
)

type commandSpy struct {
	in     financial.HTTPWagerInput
	create financial.CreateWalletInput
	calls  int
	err    error
	result financial.WagerResult
}

func (s *commandSpy) CreateWallet(_ context.Context, p identity.Principal, in financial.CreateWalletInput) (financial.CreateWalletResult, error) {
	s.calls++
	s.create = in
	if !p.Internal {
		return financial.CreateWalletResult{}, identity.ErrForbidden
	}
	if s.err != nil {
		return financial.CreateWalletResult{}, s.err
	}
	return financial.CreateWalletResult{WalletID: "wallet", PlayerID: in.PlayerID, Balance: in.InitialBalance.External(), WalletVersion: 1}, nil
}
func (s *commandSpy) GetWallet(_ context.Context, _ identity.Principal, id wallet.ID) (wallet.Wallet, error) {
	s.calls++
	if s.err != nil {
		return wallet.Wallet{}, s.err
	}
	if id == "missing" {
		return wallet.Wallet{}, ports.ErrNotFound
	}
	m, _ := money.New(10000, "BRL")
	return wallet.New(id, "player", "BRL", m)
}
func (s *commandSpy) ProcessHTTPWager(_ context.Context, p identity.Principal, in financial.HTTPWagerInput) (financial.WagerResult, error) {
	s.calls++
	s.in = in
	if p.ProviderID != string(in.ProviderID) {
		return financial.WagerResult{}, identity.ErrForbidden
	}
	if s.err != nil {
		return financial.WagerResult{}, s.err
	}
	result := s.result
	if result.TransactionID == "" {
		result = financial.WagerResult{TransactionID: "tx", ProviderID: in.ProviderID, ExternalTransactionID: in.ExternalTransactionID, WalletID: in.WalletID, Type: in.Type, State: wager.StateProcessed, Amount: in.Amount.External(), ObservedBalance: money.External{Amount: "80.00", Currency: "BRL"}, WalletVersion: 2}
	}
	return result, nil
}

type readSpy struct {
	limit  int
	cursor string
	calls  int
	err    error
}

func (s *readSpy) Ledger(_ context.Context, _ identity.Principal, id wallet.ID, cursor string, limit int) (financial.LedgerPage, error) {
	s.calls++
	s.cursor = cursor
	s.limit = limit
	if s.err != nil {
		return financial.LedgerPage{}, s.err
	}
	m, _ := money.New(10000, "BRL")
	w, _ := wallet.New(id, "player", "BRL", m)
	return financial.LedgerPage{Wallet: w, Entries: []ports.LedgerEntry{{ID: "entry", WalletID: id, TransactionID: "tx", Direction: ports.Credit, AmountCents: 10000, BalanceAfterCents: 10000, WalletVersion: 1, CreatedAt: time.Now().UTC()}}, NextCursor: "opaque"}, nil
}
func (s *readSpy) Transaction(_ context.Context, p identity.Principal, id wager.TransactionID) (financial.TransactionDetail, error) {
	s.calls++
	if s.err != nil {
		return financial.TransactionDetail{}, s.err
	}
	if id == "missing" {
		return financial.TransactionDetail{}, ports.ErrNotFound
	}
	if id == "foreign" && !p.Internal {
		return financial.TransactionDetail{}, identity.ErrForbidden
	}
	m, _ := money.New(1000, "BRL")
	owner := p.ProviderID
	if owner == "" {
		owner = "provider-a"
	}
	tx, _ := wager.NewExternal(wager.ExternalParams{ID: id, ProviderID: wager.ProviderID(owner), ExternalTransactionID: "external", PlayerID: "player", WalletID: "wallet", RoundID: "round", Type: wager.TypeBet, Amount: m, Now: time.Now().UTC()})
	_ = tx.MarkProcessed(time.Now().UTC())
	return financial.TransactionDetail{Transaction: tx}, nil
}
func (s *readSpy) ExternalTransaction(ctx context.Context, p identity.Principal, provider wager.ProviderID, external wager.ExternalTransactionID) (financial.TransactionDetail, error) {
	if string(provider) != p.ProviderID && !p.Internal {
		return financial.TransactionDetail{}, identity.ErrForbidden
	}
	return s.Transaction(ctx, p, wager.TransactionID(external))
}

func financialFixture(t *testing.T) (http.Handler, *commandSpy, *readSpy, *bytes.Buffer) {
	t.Helper()
	commands := &commandSpy{}
	reads := &readSpy{}
	auth := identity.NewAuthorizer()
	ready := &atomic.Bool{}
	ready.Store(true)
	health := NewHealthHandler(ready, stubPinger{}, config.Config{DBHealthTimeout: time.Second})
	logs := &bytes.Buffer{}
	logger := slog.New(slog.NewJSONHandler(logs, nil))
	h := NewHandler(health, logger, NewAuthMiddleware(authDouble{}, auth), &FinancialHandler{commands: commands, reads: reads, reconciliation: &reconciliationSpy{}, authorizer: auth}, nil)
	return h, commands, reads, logs
}
func callAPI(h http.Handler, method, path, token, body, key string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	r.Header.Set("Content-Type", "application/json")
	if key != "" {
		r.Header.Set("Idempotency-Key", key)
	}
	r.Header.Set(correlationIDHeader, "phase9-correlation")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

const validBet = `{"externalTransactionId":"external","playerId":"player","walletId":"wallet","type":"BET","money":{"amount":"20.00","currency":"BRL"},"roundId":"round"}`

func TestWalletHTTPContractsAndAuthorization(t *testing.T) {
	h, c, _, _ := financialFixture(t)
	for _, tc := range []struct {
		token, body string
		status      int
	}{
		{"internal", `{"playerId":"player","currency":"BRL","initialBalance":{"amount":"100.00","currency":"BRL"}}`, 201},
		{"internal", `{"playerId":"player","currency":"BRL"}`, 201},
		{"internal", `{"playerId":"","currency":"BRL"}`, 400},
		{"internal", `{"playerId":"player","currency":"bad"}`, 400},
		{"internal", `{"playerId":"player","currency":"BRL","initialBalance":{"amount":100.00,"currency":"BRL"}}`, 400},
		{"provider", `{}`, 403}, {"", `{}`, 401},
	} {
		w := callAPI(h, "POST", "/wallets", tc.token, tc.body, "")
		if w.Code != tc.status {
			t.Fatalf("wallet status %d want %d: %s", w.Code, tc.status, w.Body.String())
		}
	}
	if c.create.InitialBalance.Cents() != 0 || c.create.CorrelationID != "phase9-correlation" {
		t.Fatal("default balance/correlation mapping failed")
	}
	for _, tc := range []struct {
		path, token string
		status      int
	}{{"/wallets/wallet", "internal", 200}, {"/wallets/missing", "internal", 404}, {"/wallets/wallet", "provider", 403}, {"/wallets/wallet", "", 401}, {"/wallets/w%2Fbad", "internal", 400}} {
		w := callAPI(h, "GET", tc.path, tc.token, "", "")
		if w.Code != tc.status {
			t.Fatal("wallet read status", w.Code, tc.status)
		}
		if w.Code == 200 {
			var dto walletResponse
			if err := json.Unmarshal(w.Body.Bytes(), &dto); err != nil || dto.Balance.Amount != "100.00" || dto.Version != 1 || dto.Currency != "BRL" {
				t.Fatal("invalid wallet contract")
			}
		}
	}
	c.err = financial.ErrWalletExists
	if w := callAPI(h, "POST", "/wallets", "internal", `{"playerId":"player","currency":"BRL"}`, ""); w.Code != 409 {
		t.Fatal("wallet conflict status", w.Code)
	}
}

func TestWagerHTTPMappingAndStates(t *testing.T) {
	for _, kind := range []string{"BET", "WIN", "LOSS", "REFUND", "ROLLBACK"} {
		t.Run(kind, func(t *testing.T) {
			h, c, _, _ := financialFixture(t)
			amount := "20.00"
			if kind == "LOSS" {
				amount = "0.00"
			}
			body := strings.Replace(validBet, `"BET"`, `"`+kind+`"`, 1)
			body = strings.Replace(body, "20.00", amount, 1)
			if kind == "REFUND" || kind == "ROLLBACK" {
				body = strings.TrimSuffix(body, "}") + `,"referenceExternalTransactionId":"original"}`
			}
			w := callAPI(h, "POST", "/wagering/transactions", "provider", body, "caller-key")
			if w.Code != 201 {
				t.Fatal(w.Code, w.Body.String())
			}
			if string(c.in.Type) != kind || c.in.IdempotencyKey != "caller-key" || c.in.ProviderID != "provider-a" || c.in.CorrelationID != "phase9-correlation" {
				t.Fatal("mapping failed")
			}
			if kind == "LOSS" && !c.in.Amount.IsZero() {
				t.Fatal("LOSS amount not preserved")
			}
		})
	}
	for _, tc := range []struct {
		state  wager.State
		replay bool
		status int
	}{{wager.StateProcessed, false, 201}, {wager.StateProcessed, true, 200}, {wager.StatePendingReference, false, 202}, {wager.StatePendingReference, true, 202}, {wager.StateRejected, false, 422}, {wager.StateRejected, true, 422}} {
		h, c, _, _ := financialFixture(t)
		c.result = financial.WagerResult{TransactionID: "tx", WalletID: "wallet", State: tc.state, IdempotentReplay: tc.replay, FailureCode: financial.FailureInsufficientFunds, ObservedBalance: money.External{Amount: "80.00", Currency: "BRL"}}
		w := callAPI(h, "POST", "/wagering/transactions", "provider", validBet, "key")
		if w.Code != tc.status {
			t.Fatalf("state %s status %d", tc.state, w.Code)
		}
		var result financial.WagerResult
		_ = json.Unmarshal(w.Body.Bytes(), &result)
		if result.ObservedBalance.Amount != "80.00" || result.IdempotentReplay != tc.replay {
			t.Fatal("result reconstructed instead of mapping saved result")
		}
	}
}

func TestHTTPBoundaryRejectsInvalidRequestsBeforeCommand(t *testing.T) {
	for name, body := range map[string]string{"OPENING": strings.Replace(validBet, "BET", "OPENING", 1), "unknown type": strings.Replace(validBet, "BET", "MINT", 1), "number": strings.Replace(validBet, `"20.00"`, `20.00`, 1), "one decimal": strings.Replace(validBet, "20.00", "20.0", 1), "integer": strings.Replace(validBet, "20.00", "20", 1), "three decimals": strings.Replace(validBet, "20.00", "20.000", 1), "scientific": strings.Replace(validBet, "20.00", "2e1", 1), "negative": strings.Replace(validBet, "20.00", "-20.00", 1), "NaN": strings.Replace(validBet, "20.00", "NaN", 1), "Infinity": strings.Replace(validBet, "20.00", "Infinity", 1), "overflow": strings.Replace(validBet, "20.00", "92233720368547758.08", 1), "missing currency": strings.Replace(validBet, `,"currency":"BRL"`, "", 1), "unknown field": strings.TrimSuffix(validBet, "}") + `,"unexpected":true}`, "trailing": validBet + " {}", "garbage": validBet + "garbage", "empty": "", "null": "null", "malformed": "{", "invalid ID": strings.Replace(validBet, `"player"`, `" player"`, 1)} {
		t.Run(name, func(t *testing.T) {
			h, c, _, _ := financialFixture(t)
			w := callAPI(h, "POST", "/wagering/transactions", "provider", body, "key")
			if w.Code != 400 || c.calls != 0 {
				t.Fatalf("status=%d calls=%d", w.Code, c.calls)
			}
		})
	}
	h, c, _, _ := financialFixture(t)
	w := callAPI(h, "POST", "/wagering/transactions", "provider", validBet, "")
	if w.Code != 400 || c.calls != 0 {
		t.Fatal("missing key accepted")
	}
	body := strings.TrimSuffix(validBet, "}") + `,"providerId":"provider-b"}`
	w = callAPI(h, "POST", "/wagering/transactions", "provider", body, "key")
	if w.Code != 403 || c.calls != 0 {
		t.Fatal("body spoof accepted")
	}
	if w := callAPI(h, "POST", "/wagering/transactions", "internal", validBet, "key"); w.Code != 403 {
		t.Fatal("internal impersonated provider")
	}
	for _, contentType := range []string{"", "text/plain", "application/json; charset=latin1"} {
		r := httptest.NewRequest("POST", "/wagering/transactions", strings.NewReader(validBet))
		r.Header.Set("Authorization", "Bearer provider")
		r.Header.Set("Idempotency-Key", "key")
		if contentType != "" {
			r.Header.Set("Content-Type", contentType)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 415 {
			t.Fatal("content type accepted", contentType, w.Code)
		}
	}
	big := `{"externalTransactionId":"` + strings.Repeat("a", maxJSONBody) + `"}`
	if w := callAPI(h, "POST", "/wagering/transactions", "provider", big, "key"); w.Code != 413 {
		t.Fatal("body limit not enforced", w.Code)
	}
	r := httptest.NewRequest("POST", "/wagering/transactions", strings.NewReader(validBet))
	r.Header.Set("Authorization", "Bearer provider")
	r.Header.Set("Content-Type", "application/json")
	r.Header.Add("Idempotency-Key", "first")
	r.Header.Add("Idempotency-Key", "second")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	if rec.Code != 400 {
		t.Fatal("multiple idempotency headers accepted")
	}
}

func TestLedgerHTTPPaginationValidation(t *testing.T) {
	for _, tc := range []struct {
		query         string
		status, limit int
	}{{"", 200, 50}, {"?limit=100", 200, 100}, {"?limit=1&cursor=opaque", 200, 1}, {"?limit=0", 400, 0}, {"?limit=-1", 400, 0}, {"?limit=101", 400, 0}, {"?limit=999999999999999999999999", 400, 0}, {"?limit=no", 400, 0}, {"?limit=1&limit=2", 400, 0}, {"?cursor=", 400, 0}, {"?cursor=a&cursor=b", 400, 0}, {"?limit=50;garbage=1", 400, 0}, {"?unexpected=1", 400, 0}} {
		h, _, reads, _ := financialFixture(t)
		w := callAPI(h, "GET", "/wallets/wallet/ledger"+tc.query, "internal", "", "")
		if w.Code != tc.status {
			t.Fatalf("%s status %d", tc.query, w.Code)
		}
		if w.Code == 200 {
			if reads.limit != tc.limit {
				t.Fatal("limit not forwarded")
			}
			var page ledgerResponse
			_ = json.Unmarshal(w.Body.Bytes(), &page)
			if page.Items[0].Amount.Amount != "100.00" || page.Items[0].BalanceBefore.Amount != "0.00" || page.NextCursor != "opaque" {
				t.Fatal("ledger contract")
			}
		}
	}
	h, _, reads, _ := financialFixture(t)
	reads.err = financial.ErrInvalidPagination
	if w := callAPI(h, "GET", "/wallets/wallet/ledger?cursor=bad", "internal", "", ""); w.Code != 400 {
		t.Fatal("bad cursor accepted")
	}
	if w := callAPI(h, "GET", "/wallets/wallet/ledger", "provider", "", ""); w.Code != 403 {
		t.Fatal("provider read ledger")
	}
}

func TestTransactionLookupsHTTPAndErrors(t *testing.T) {
	h, c, _, _ := financialFixture(t)
	for _, tc := range []struct {
		path, token string
		status      int
	}{{"/wagering/transactions/tx", "provider", 200}, {"/wagering/transactions/missing", "provider", 404}, {"/wagering/transactions/foreign", "provider", 403}, {"/providers/provider-a/wagering/transactions/external", "provider", 200}, {"/providers/provider-b/wagering/transactions/external", "provider", 403}, {"/wagering/transactions/tx", "no-role", 403}, {"/wagering/transactions/tx", "", 401}, {"/wagering/transactions/tx%2Fbad", "provider", 400}} {
		w := callAPI(h, "GET", tc.path, tc.token, "", "")
		if w.Code != tc.status {
			t.Fatalf("%s status %d", tc.path, w.Code)
		}
	}
	for _, tc := range []struct {
		err    error
		status int
		code   ErrorCode
	}{{financial.ErrIdempotencyConflict, 409, "IDEMPOTENCY_CONFLICT"}, {financial.ErrDuplicateExternalTransaction, 409, "DUPLICATE_EXTERNAL_TRANSACTION"}, {financial.ErrInvalidInput, 400, "INVALID_REQUEST"}, {financial.ErrWalletNotFound, 404, ErrorCodeNotFound}, {financial.ErrPersistence, 503, "SERVICE_UNAVAILABLE"}, {context.DeadlineExceeded, 503, "SERVICE_UNAVAILABLE"}, {errors.New("secret SQL stack token"), 500, ErrorCodeInternal}} {
		c.err = tc.err
		w := callAPI(h, "POST", "/wagering/transactions", "provider", validBet, "key")
		var e errorResponse
		_ = json.Unmarshal(w.Body.Bytes(), &e)
		if w.Code != tc.status || e.Error.Code != tc.code || e.CorrelationID != "phase9-correlation" {
			t.Fatal("error contract", w.Code, w.Body.String())
		}
		if strings.Contains(w.Body.String(), "secret") {
			t.Fatal("technical details leaked")
		}
	}
}

func TestPublicHealthReconciliationAndStructuredLogging(t *testing.T) {
	h, _, _, logs := financialFixture(t)
	for _, path := range []string{"/health/live", "/health/ready"} {
		w := callAPI(h, "GET", path, "", "", "")
		if w.Code != 200 {
			t.Fatal("health requires auth")
		}
		if w.Header().Get("X-Content-Type-Options") != "nosniff" {
			t.Fatal("missing security header")
		}
	}
	for _, tc := range []struct {
		token  string
		status int
	}{{"internal", 200}, {"provider", 403}, {"", 401}} {
		if w := callAPI(h, "POST", "/wallets/wallet/reconciliation", tc.token, "", ""); w.Code != tc.status {
			t.Fatal("reconciliation status", w.Code)
		}
	}
	if w := callAPI(h, "PUT", "/wallets/wallet", "internal", "", ""); w.Code != 405 || w.Header().Get("Allow") == "" {
		t.Fatal("method routing")
	}
	logs.Reset()
	w := callAPI(h, "POST", "/wagering/transactions", "provider", validBet, "SECRET-IDEMPOTENCY-KEY")
	if w.Code != 201 {
		t.Fatal(w.Code)
	}
	for _, secret := range []string{"SECRET-IDEMPOTENCY-KEY", "Bearer ", "20.00"} {
		if strings.Contains(logs.String(), secret) {
			t.Fatal("sensitive value logged")
		}
	}
	var event map[string]any
	if err := json.NewDecoder(logs).Decode(&event); err != nil {
		t.Fatal(err)
	}
	if event["route"] != "POST /wagering/transactions" || event["status"] != float64(201) || event["providerId"] != "provider-a" || event["transactionId"] != "tx" || event["walletId"] != "wallet" || event["correlationId"] != "phase9-correlation" {
		t.Fatal("logging fields missing", event)
	}
	logs.Reset()
	if w := callAPI(h, "GET", "/wagering/transactions/tx", "internal", "", ""); w.Code != 200 {
		t.Fatal("internal lookup denied")
	}
	if err := json.NewDecoder(logs).Decode(&event); err != nil || event["providerId"] != "provider-a" {
		t.Fatal("resource provider missing from internal read log", err)
	}
}
