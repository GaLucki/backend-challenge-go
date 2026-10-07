package application_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	httpadapter "github.com/junglegaming/backend-challenge-go/internal/adapter/http"
	"github.com/junglegaming/backend-challenge-go/internal/application"
	"github.com/junglegaming/backend-challenge-go/internal/application/financial"
	"github.com/junglegaming/backend-challenge-go/internal/domain/money"
	"github.com/junglegaming/backend-challenge-go/internal/domain/wager"
	"github.com/junglegaming/backend-challenge-go/internal/identity"
	"github.com/junglegaming/backend-challenge-go/internal/ports"
	"go.uber.org/fx"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/clientcredentials"
)

func phase8Port(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := strconv.Itoa(ln.Addr().(*net.TCPAddr).Port)
	_ = ln.Close()
	return port
}

func phase8Environment(t *testing.T, issuer string) string {
	t.Helper()
	t.Setenv("DATABASE_URL", isolatedPublisherDatabase(t))
	port := phase8Port(t)
	t.Setenv("HTTP_PORT", port)
	t.Setenv("APP_ENV", "test")
	t.Setenv("LOG_LEVEL", "error")
	t.Setenv("SQS_ENABLED", "false")
	t.Setenv("OUTBOX_PUBLISHER_ENABLED", "false")
	t.Setenv("OIDC_ENABLED", "true")
	t.Setenv("OIDC_ISSUER_URL", issuer)
	t.Setenv("OIDC_AUDIENCE", "wagering-api")
	t.Setenv("OIDC_HTTP_TIMEOUT", "2s")
	return "http://127.0.0.1:" + port
}

func TestIntegrationOIDCProviderIsolation(t *testing.T) {
	issuer := os.Getenv("TEST_OIDC_ISSUER_URL")
	if issuer == "" {
		t.Skip("set TEST_OIDC_ISSUER_URL and TEST_DATABASE_URL for real identity/data isolation")
	}
	base := phase8Environment(t, issuer)
	var secured *financial.AuthorizedService
	var authenticator identity.Authenticator
	var pool *pgxpool.Pool
	app := fx.New(fx.NopLogger, application.Module, fx.Populate(&secured, &authenticator, &pool))
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	if err := app.Start(ctx); err != nil {
		t.Fatal("Fx OIDC startup failed", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := app.Stop(ctx); err != nil {
			t.Error(err)
		}
	})
	client := &http.Client{Timeout: 5 * time.Second}
	tokens := map[string]string{}
	principals := map[string]identity.Principal{}
	for _, id := range []string{"provider-a", "provider-b", "internal-service"} {
		grant := clientcredentials.Config{ClientID: id, ClientSecret: "local-dev-" + id + "-secret", TokenURL: strings.TrimRight(issuer, "/") + "/protocol/openid-connect/token"}
		token, err := grant.Token(context.WithValue(ctx, oauth2.HTTPClient, client))
		if err != nil {
			t.Fatalf("real client_credentials failed for %s", id)
		}
		tokens[id] = token.AccessToken
		p, err := authenticator.Authenticate(ctx, token.AccessToken)
		if err != nil {
			t.Fatal("real token rejected")
		}
		principals[id] = p
	}
	request := func(url, token string, want int) {
		t.Helper()
		r, _ := http.NewRequestWithContext(ctx, "GET", url, nil)
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		resp, err := client.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != want {
			t.Fatalf("%s status=%d want=%d", url, resp.StatusCode, want)
		}
	}
	for _, path := range []string{"/health/live", "/health/ready"} {
		request(base+path, "", 200)
	}
	request(base+"/auth/me", "", 401)
	request(base+"/auth/me", "invalid", 401)
	for _, id := range []string{"provider-a", "provider-b", "internal-service"} {
		request(base+"/auth/me", tokens[id], 200)
	}
	for _, id := range []string{"provider-a", "provider-b"} {
		request(base+"/auth/providers/"+id, tokens[id], 200)
		foreign := "provider-a"
		if id == foreign {
			foreign = "provider-b"
		}
		request(base+"/auth/providers/"+foreign, tokens[id], 403)
		request(base+"/auth/internal", tokens[id], 403)
	}
	request(base+"/auth/internal", tokens["internal-service"], 200)
	request(base+"/auth/providers/provider-a", tokens["internal-service"], 200)
	initial, _ := money.New(10000, "BRL")
	wallet, err := secured.CreateWallet(ctx, principals["internal-service"], financial.CreateWalletInput{PlayerID: "phase8-player", Currency: "BRL", InitialBalance: initial})
	if err != nil {
		t.Fatal(err)
	}
	amount, _ := money.New(100, "BRL")
	input := financial.HTTPWagerInput{WagerInput: financial.WagerInput{PlayerID: "phase8-player", WalletID: wallet.WalletID, ExternalTransactionID: "same-external", RoundID: "round", Type: wager.TypeBet, Amount: amount}, IdempotencyKey: "same-key"}
	results := map[string]financial.WagerResult{}
	for _, id := range []string{"provider-a", "provider-b"} {
		first, err := secured.ProcessHTTPWager(ctx, principals[id], input)
		if err != nil {
			t.Fatal(err)
		}
		if string(first.ProviderID) != id || first.IdempotentReplay {
			t.Fatal("incorrect identity or foreign replay")
		}
		replay, err := secured.ProcessHTTPWager(ctx, principals[id], input)
		if err != nil || !replay.IdempotentReplay || replay.TransactionID != first.TransactionID {
			t.Fatal("own persisted replay failed", err)
		}
		changed := input
		changed.Amount, _ = money.New(101, "BRL")
		if _, err := secured.ProcessHTTPWager(ctx, principals[id], changed); !errors.Is(err, financial.ErrIdempotencyConflict) {
			t.Fatal("own payload conflict not enforced", err)
		}
		foreign := "provider-a"
		if id == foreign {
			foreign = "provider-b"
		}
		spoof := input
		spoof.ProviderID = wager.ProviderID(foreign)
		if _, err := secured.ProcessHTTPWager(ctx, principals[id], spoof); !errors.Is(err, identity.ErrForbidden) {
			t.Fatal("real token provider spoof/replay accepted", err)
		}
		results[id] = first
	}
	if results["provider-a"].TransactionID == results["provider-b"].TransactionID {
		t.Fatal("providers shared transaction/replay")
	}
	// This test-only router performs authentication alone. Ownership checks
	// therefore have to execute in the application service, including ID reads.
	middleware := httpadapter.NewAuthMiddleware(authenticator, identity.NewAuthorizer())
	mux := http.NewServeMux()
	respond := func(w http.ResponseWriter, tx wager.Transaction, err error) {
		if err != nil {
			status := 500
			code := httpadapter.ErrorCodeInternal
			if errors.Is(err, identity.ErrForbidden) {
				status = 403
				code = httpadapter.ErrorCodeForbidden
			}
			if errors.Is(err, ports.ErrNotFound) {
				status = 404
				code = httpadapter.ErrorCodeNotFound
			}
			httpadapter.WriteError(w, status, code, "request denied")
			return
		}
		httpadapter.WriteJSON(w, 200, map[string]any{"transactionId": tx.ID(), "providerId": tx.ProviderID()})
	}
	mux.HandleFunc("GET /test/transactions/{id}", func(w http.ResponseWriter, r *http.Request) {
		p, _ := identity.FromContext(r.Context())
		tx, err := secured.GetTransaction(r.Context(), p, wager.TransactionID(r.PathValue("id")))
		respond(w, tx, err)
	})
	mux.HandleFunc("GET /test/providers/{provider}/transactions/{external}", func(w http.ResponseWriter, r *http.Request) {
		p, _ := identity.FromContext(r.Context())
		tx, err := secured.GetByExternalID(r.Context(), p, wager.ProviderID(r.PathValue("provider")), wager.ExternalTransactionID(r.PathValue("external")))
		respond(w, tx, err)
	})
	mux.HandleFunc("POST /test/provider-spoof", func(w http.ResponseWriter, r *http.Request) {
		var dto struct {
			ProviderID wager.ProviderID `json:"providerId"`
		}
		if err := json.NewDecoder(r.Body).Decode(&dto); err != nil {
			w.WriteHeader(400)
			return
		}
		p, _ := identity.FromContext(r.Context())
		in := input
		in.ProviderID = dto.ProviderID
		_, err := secured.ProcessHTTPWager(r.Context(), p, in)
		if errors.Is(err, identity.ErrForbidden) {
			httpadapter.WriteError(w, 403, httpadapter.ErrorCodeForbidden, "access forbidden")
			return
		}
		t.Error("spoof reached financial core")
		w.WriteHeader(500)
	})
	server := httptest.NewServer(middleware.Authenticate(mux))
	defer server.Close()
	for _, id := range []string{"provider-a", "provider-b"} {
		foreign := "provider-a"
		if id == foreign {
			foreign = "provider-b"
		}
		request(server.URL+"/test/transactions/"+string(results[id].TransactionID), tokens[id], 200)
		request(server.URL+"/test/transactions/"+string(results[foreign].TransactionID), tokens[id], 403)
		request(server.URL+"/test/providers/"+id+"/transactions/same-external", tokens[id], 200)
		request(server.URL+"/test/providers/"+foreign+"/transactions/same-external", tokens[id], 403)
		request(server.URL+"/test/transactions/"+string(wallet.OpeningTransactionID), tokens[id], 403)
		body, _ := json.Marshal(map[string]string{"providerId": foreign})
		r, _ := http.NewRequestWithContext(ctx, "POST", server.URL+"/test/provider-spoof", bytes.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+tokens[id])
		resp, err := client.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != 403 {
			t.Fatalf("body spoof status %d", resp.StatusCode)
		}
		if _, err := secured.GetWallet(ctx, principals[id], wallet.WalletID); !errors.Is(err, identity.ErrForbidden) {
			t.Fatal("provider read internal wallet")
		}
		if _, err := secured.CreateWallet(ctx, principals[id], financial.CreateWalletInput{}); !errors.Is(err, identity.ErrForbidden) {
			t.Fatal("provider created internal wallet")
		}
	}
	request(server.URL+"/test/transactions/missing", tokens["provider-a"], 404)
	request(server.URL+"/test/transactions/"+string(wallet.OpeningTransactionID), tokens["internal-service"], 200)
	request(server.URL+"/test/transactions/"+string(results["provider-b"].TransactionID), tokens["internal-service"], 200)
	loaded, err := secured.GetWallet(ctx, principals["internal-service"], wallet.WalletID)
	if err != nil || loaded.Balance().Cents() != 9800 || loaded.Version() != 3 {
		t.Fatal("unauthorized request/replay changed wallet", err)
	}
	var keys, txs, ledger, outbox int
	if err := pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM wager_idempotency_records),(SELECT count(*) FROM wager_transactions),(SELECT count(*) FROM wallet_ledger_entries),(SELECT count(*) FROM outbox_events)`).Scan(&keys, &txs, &ledger, &outbox); err != nil {
		t.Fatal(err)
	}
	if keys != 2 || txs != 3 || ledger != 3 || outbox != 6 {
		t.Fatalf("unexpected persisted effects: keys=%d tx=%d ledger=%d outbox=%d", keys, txs, ledger, outbox)
	}
}

func TestIntegrationFxOIDCDiscoveryFailurePreventsHTTPStartup(t *testing.T) {
	idp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(503) }))
	defer idp.Close()
	base := phase8Environment(t, idp.URL)
	app := fx.New(fx.NopLogger, application.Module)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := app.Start(ctx); err == nil {
		_ = app.Stop(ctx)
		t.Fatal("startup succeeded without OIDC discovery")
	}
	client := http.Client{Timeout: time.Second}
	if resp, err := client.Get(base + "/health/live"); err == nil {
		_ = resp.Body.Close()
		t.Fatal("HTTP started before identity validation was available")
	}
}
