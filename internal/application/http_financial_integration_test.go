package application_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awssqs "github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/jackc/pgx/v5/pgxpool"
	sqsadapter "github.com/junglegaming/backend-challenge-go/internal/adapter/sqs"
	"github.com/junglegaming/backend-challenge-go/internal/application"
	"github.com/junglegaming/backend-challenge-go/internal/application/financial"
	"github.com/junglegaming/backend-challenge-go/internal/config"
	"github.com/junglegaming/backend-challenge-go/internal/domain/money"
	"github.com/junglegaming/backend-challenge-go/internal/domain/wager"
	"github.com/junglegaming/backend-challenge-go/internal/observability"
	"go.uber.org/fx"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/clientcredentials"
)

type apiFixture struct {
	ctx       context.Context
	app       *fx.App
	base      string
	client    *http.Client
	tokens    map[string]string
	pool      *pgxpool.Pool
	core      *financial.Service
	metrics   *observability.Metrics
	collector *application.OperationalCollector
}

func newAPIFixture(t *testing.T) *apiFixture {
	t.Helper()
	issuer := os.Getenv("TEST_OIDC_ISSUER_URL")
	if issuer == "" {
		t.Skip("set TEST_DATABASE_URL and TEST_OIDC_ISSUER_URL for real HTTP financial integration")
	}
	f := &apiFixture{base: phase8Environment(t, issuer), client: &http.Client{Timeout: 12 * time.Second}, tokens: map[string]string{}}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	f.ctx = ctx
	t.Cleanup(cancel)
	f.start(t)
	t.Cleanup(func() { f.stop(t) })
	for _, id := range []string{"provider-a", "provider-b", "internal-service"} {
		grant := clientcredentials.Config{ClientID: id, ClientSecret: "local-dev-" + id + "-secret", TokenURL: issuer + "/protocol/openid-connect/token"}
		token, err := grant.Token(context.WithValue(ctx, oauth2.HTTPClient, f.client))
		if err != nil {
			t.Fatalf("client_credentials failed for %s", id)
		}
		f.tokens[id] = token.AccessToken
	}
	return f
}
func (f *apiFixture) start(t *testing.T) {
	t.Helper()
	f.app = fx.New(fx.NopLogger, application.Module, fx.Populate(&f.pool, &f.core, &f.metrics, &f.collector))
	ctx, cancel := context.WithTimeout(f.ctx, 10*time.Second)
	defer cancel()
	if err := f.app.Start(ctx); err != nil {
		t.Fatal(err)
	}
}
func (f *apiFixture) stop(t *testing.T) {
	t.Helper()
	if f.app == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := f.app.Stop(ctx); err != nil {
		t.Error(err)
	}
	f.app = nil
}
func (f *apiFixture) request(t *testing.T, method, path, identity string, payload any, key string, want int) []byte {
	t.Helper()
	var body []byte
	if payload != nil {
		var err error
		body, err = json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
	}
	r, err := http.NewRequestWithContext(f.ctx, method, f.base+path, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if identity != "" {
		r.Header.Set("Authorization", "Bearer "+f.tokens[identity])
	}
	if payload != nil {
		r.Header.Set("Content-Type", "application/json")
	}
	if key != "" {
		r.Header.Set("Idempotency-Key", key)
	}
	r.Header.Set("X-Correlation-ID", "phase9-real")
	response, err := f.client.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != want {
		t.Fatalf("%s %s status=%d want=%d response=%s", method, path, response.StatusCode, want, raw)
	}
	if response.Header.Get("X-Correlation-ID") != "phase9-real" || response.Header.Get("X-Content-Type-Options") != "nosniff" {
		t.Fatal("response headers missing")
	}
	return raw
}
func (f *apiFixture) wallet(t *testing.T, player, amount string) financial.CreateWalletResult {
	t.Helper()
	raw := f.request(t, "POST", "/wallets", "internal-service", map[string]any{"playerId": player, "currency": "BRL", "initialBalance": money.External{Amount: amount, Currency: "BRL"}}, "", 201)
	var result financial.CreateWalletResult
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	return result
}
func wagerPayload(wallet, player, external, kind, amount, reference string) map[string]any {
	body := map[string]any{"walletId": wallet, "playerId": player, "externalTransactionId": external, "type": kind, "money": money.External{Amount: amount, Currency: "BRL"}, "roundId": "round"}
	if reference != "" {
		body["referenceExternalTransactionId"] = reference
	}
	return body
}
func (f *apiFixture) wager(t *testing.T, provider string, body map[string]any, key string, want int) financial.WagerResult {
	t.Helper()
	raw := f.request(t, "POST", "/wagering/transactions", provider, body, key, want)
	var result financial.WagerResult
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func TestIntegrationFinalFinancialHTTP(t *testing.T) {
	f := newAPIFixture(t)
	for _, path := range []string{"/health/live", "/health/ready"} {
		f.request(t, "GET", path, "", nil, "", 200)
	}
	f.request(t, "POST", "/wallets", "provider-a", map[string]any{}, "", 403)
	f.request(t, "POST", "/wallets", "provider-b", map[string]any{}, "", 403)
	w := f.wallet(t, "phase9-player", "100.00")
	wid := string(w.WalletID)
	zero := f.wallet(t, "phase9-zero", "0.00")
	if zero.OpeningTransactionID != "" {
		t.Fatal("zero balance created OPENING")
	}
	raw := f.request(t, "GET", "/wallets/"+string(zero.WalletID)+"/ledger", "internal-service", nil, "", 200)
	if !bytes.Contains(raw, []byte(`"items":[]`)) {
		t.Fatal("zero wallet ledger not empty")
	}
	f.request(t, "GET", "/wallets/"+wid, "internal-service", nil, "", 200)
	for _, provider := range []string{"provider-a", "provider-b"} {
		f.request(t, "GET", "/wallets/"+wid, provider, nil, "", 403)
		f.request(t, "GET", "/wallets/"+wid+"/ledger", provider, nil, "", 403)
	}
	f.request(t, "GET", "/wallets/missing", "internal-service", nil, "", 404)
	f.request(t, "POST", "/wallets", "internal-service", map[string]any{"playerId": "phase9-player", "currency": "BRL"}, "", 409)
	betBody := wagerPayload(wid, "phase9-player", "bet-1", "BET", "20.00", "")
	f.request(t, "POST", "/wagering/transactions", "provider-a", betBody, "", 400)
	f.request(t, "POST", "/wagering/transactions", "", betBody, "key", 401)
	f.request(t, "POST", "/wagering/transactions", "internal-service", betBody, "key", 403)
	bet := f.wager(t, "provider-a", betBody, "bet-key", 201)
	if bet.ObservedBalance.Amount != "80.00" {
		t.Fatal("BET balance")
	}
	win := f.wager(t, "provider-a", wagerPayload(wid, "phase9-player", "win-1", "WIN", "50.00", ""), "win-key", 201)
	if win.ObservedBalance.Amount != "130.00" {
		t.Fatal("WIN balance")
	}
	replay := f.wager(t, "provider-a", betBody, "bet-key", 200)
	if !replay.IdempotentReplay || replay.TransactionID != bet.TransactionID || replay.ObservedBalance.Amount != "80.00" || replay.WalletVersion != bet.WalletVersion {
		t.Fatal("original observed balance lost")
	}
	changed := wagerPayload(wid, "phase9-player", "bet-1", "BET", "21.00", "")
	f.request(t, "POST", "/wagering/transactions", "provider-a", changed, "bet-key", 409)
	f.request(t, "POST", "/wagering/transactions", "provider-a", betBody, "different-key", 409)
	loss := f.wager(t, "provider-a", wagerPayload(wid, "phase9-player", "loss-1", "LOSS", "0.00", ""), "loss-key", 201)
	if loss.WalletVersion != win.WalletVersion || loss.ObservedBalance.Amount != "130.00" {
		t.Fatal("LOSS changed wallet")
	}
	f.request(t, "POST", "/wagering/transactions", "provider-a", wagerPayload(wid, "phase9-player", "bad-loss", "LOSS", "1.00", ""), "bad-loss", 400)
	f.request(t, "POST", "/wagering/transactions", "provider-a", wagerPayload(wid, "phase9-player", "bad-refund", "REFUND", "20.00", ""), "bad-refund", 400)
	refund := f.wager(t, "provider-a", wagerPayload(wid, "phase9-player", "refund-1", "REFUND", "20.00", "bet-1"), "refund-key", 201)
	if refund.ObservedBalance.Amount != "150.00" {
		t.Fatal("REFUND balance")
	}
	rollback := f.wager(t, "provider-a", wagerPayload(wid, "phase9-player", "rollback-1", "ROLLBACK", "50.00", "win-1"), "rollback-key", 201)
	if rollback.ObservedBalance.Amount != "100.00" {
		t.Fatal("ROLLBACK balance")
	}
	f.request(t, "POST", "/wagering/transactions", "provider-a", wagerPayload(wid, "phase9-player", "opening", "OPENING", "1.00", ""), "opening", 400)
	f.request(t, "POST", "/wagering/transactions", "provider-a", wagerPayload(wid, "phase9-player", "scale", "BET", "1.0", ""), "scale", 400)
	// Lookup must return the saved original observed balance, not current wallet.
	raw = f.request(t, "GET", "/wagering/transactions/"+string(bet.TransactionID), "provider-a", nil, "", 200)
	var detail struct {
		TransactionID string                 `json:"transactionId"`
		Result        *financial.WagerResult `json:"result"`
	}
	if err := json.Unmarshal(raw, &detail); err != nil || detail.Result == nil || detail.Result.ObservedBalance.Amount != "80.00" {
		t.Fatal("lookup lost stored snapshot")
	}
	f.request(t, "GET", "/providers/provider-a/wagering/transactions/bet-1", "provider-a", nil, "", 200)
	f.request(t, "GET", "/wagering/transactions/"+string(w.OpeningTransactionID), "internal-service", nil, "", 200)
	f.request(t, "GET", "/wagering/transactions/"+string(w.OpeningTransactionID), "provider-a", nil, "", 403)
	f.request(t, "GET", "/wagering/transactions/missing", "provider-a", nil, "", 404)
	// Cursor remains usable when another movement is appended between pages.
	type pageDTO struct {
		Items []struct {
			ID        string         `json:"ledgerEntryId"`
			Version   int64          `json:"walletVersion"`
			Amount    money.External `json:"amount"`
			Direction string         `json:"direction"`
		} `json:"items"`
		NextCursor string `json:"nextCursor"`
	}
	var page pageDTO
	raw = f.request(t, "GET", "/wallets/"+wid+"/ledger?limit=2", "internal-service", nil, "", 200)
	if err := json.Unmarshal(raw, &page); err != nil || len(page.Items) != 2 || page.NextCursor == "" {
		t.Fatal("initial ledger page", err)
	}
	seen := map[string]bool{}
	sum := int64(0)
	lastVersion := int64(0)
	consume := func() {
		for _, item := range page.Items {
			if seen[item.ID] || item.Version <= lastVersion {
				t.Fatal("unstable ledger order")
			}
			seen[item.ID] = true
			lastVersion = item.Version
			m, err := money.ParseDecimal(item.Amount.Amount, item.Amount.Currency)
			if err != nil {
				t.Fatal(err)
			}
			if item.Direction == "CREDIT" {
				sum += m.Cents()
			} else {
				sum -= m.Cents()
			}
		}
	}
	consume()
	betB := f.wager(t, "provider-b", wagerPayload(wid, "phase9-player", "bet-1", "BET", "10.00", ""), "bet-key", 201)
	if betB.TransactionID == bet.TransactionID {
		t.Fatal("providers shared replay")
	}
	for _, pair := range []struct {
		own, other string
		tx         wager.TransactionID
	}{{"provider-a", "provider-b", betB.TransactionID}, {"provider-b", "provider-a", bet.TransactionID}} {
		f.request(t, "GET", "/wagering/transactions/"+string(pair.tx), pair.own, nil, "", 403)
		f.request(t, "GET", "/providers/"+pair.other+"/wagering/transactions/bet-1", pair.own, nil, "", 403)
		spoof := wagerPayload(wid, "phase9-player", "spoof", "BET", "1.00", "")
		spoof["providerId"] = pair.other
		f.request(t, "POST", "/wagering/transactions", pair.own, spoof, "spoof-key", 403)
	}
	f.request(t, "GET", "/wagering/transactions/"+string(betB.TransactionID), "provider-b", nil, "", 200)
	f.request(t, "GET", "/providers/provider-b/wagering/transactions/bet-1", "provider-b", nil, "", 200)
	for page.NextCursor != "" {
		cursor := page.NextCursor
		page = pageDTO{}
		raw = f.request(t, "GET", "/wallets/"+wid+"/ledger?limit=2&cursor="+url.QueryEscape(cursor), "internal-service", nil, "", 200)
		if err := json.Unmarshal(raw, &page); err != nil {
			t.Fatal(err)
		}
		consume()
	}
	if len(seen) != 6 || sum != 9000 {
		t.Fatal("ledger does not match movements", len(seen), sum)
	}
	f.request(t, "GET", "/wallets/"+wid+"/ledger?cursor=bad", "internal-service", nil, "", 400)
	f.request(t, "GET", "/wallets/"+wid+"/ledger?limit=101", "internal-service", nil, "", 400)
	rejectedBody := wagerPayload(wid, "phase9-player", "rejected", "BET", "200.00", "")
	rejected := f.wager(t, "provider-a", rejectedBody, "reject-key", 422)
	if rejected.State != wager.StateRejected || rejected.FailureCode != financial.FailureInsufficientFunds {
		t.Fatal("business rejection not persisted")
	}
	rejectedReplay := f.wager(t, "provider-a", rejectedBody, "reject-key", 422)
	if !rejectedReplay.IdempotentReplay || rejectedReplay.TransactionID != rejected.TransactionID {
		t.Fatal("rejected replay")
	}
	pendingBody := wagerPayload(wid, "phase9-player", "pending-refund", "REFUND", "5.00", "future-bet")
	pending := f.wager(t, "provider-a", pendingBody, "pending-key", 202)
	if pending.State != wager.StatePendingReference {
		t.Fatal("pending mapping")
	}
	f.request(t, "GET", "/wagering/transactions/"+string(pending.TransactionID), "provider-a", nil, "", 200)
	f.wager(t, "provider-a", wagerPayload(wid, "phase9-player", "future-bet", "BET", "5.00", ""), "future-key", 201)
	deadline := time.Now().Add(6 * time.Second)
	for {
		var state string
		if err := f.pool.QueryRow(f.ctx, `SELECT state FROM wager_transactions WHERE transaction_id=$1`, string(pending.TransactionID)).Scan(&state); err != nil {
			t.Fatal(err)
		}
		if state == "PROCESSED" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("pending worker did not resolve reference")
		}
		time.Sleep(50 * time.Millisecond)
	}
	resolved := f.wager(t, "provider-a", pendingBody, "pending-key", 200)
	if resolved.State != wager.StateProcessed || !resolved.IdempotentReplay {
		t.Fatal("resolved pending replay")
	}
	f.request(t, "GET", "/wagering/transactions/"+string(pending.TransactionID), "provider-a", nil, "", 200)
	for _, provider := range []string{"provider-a", "provider-b"} {
		f.request(t, "POST", "/wallets/"+wid+"/reconciliation", provider, nil, "", 403)
	}
	raw = f.request(t, "POST", "/wallets/"+wid+"/reconciliation", "internal-service", nil, "", 200)
	var audit financial.ReconciliationResult
	if err := json.Unmarshal(raw, &audit); err != nil || audit.Status != "CONSISTENT" || len(audit.Divergences) != 0 {
		t.Fatal("financial history failed reconciliation", err, string(raw))
	}
	var balance, ledgerSum int64
	var ledgerCount, records int
	if err := f.pool.QueryRow(f.ctx, `SELECT w.balance_cents,(SELECT coalesce(sum(CASE WHEN direction='CREDIT' THEN amount_cents ELSE -amount_cents END),0) FROM wallet_ledger_entries WHERE wallet_id=w.id),(SELECT count(*) FROM wallet_ledger_entries WHERE wallet_id=w.id),(SELECT count(*) FROM wager_idempotency_records) FROM wallets w WHERE w.id=$1`, wid).Scan(&balance, &ledgerSum, &ledgerCount, &records); err != nil {
		t.Fatal(err)
	}
	if balance != 9000 || ledgerSum != balance || ledgerCount != 8 || records != 9 {
		t.Fatalf("unexpected financial effects balance=%d ledger=%d count=%d keys=%d", balance, ledgerSum, ledgerCount, records)
	}
	var correlationID string
	if err := f.pool.QueryRow(f.ctx, `SELECT payload->>'correlationId' FROM outbox_events WHERE event_id=$1`, string(bet.TransactionID)+":WalletBalanceChanged").Scan(&correlationID); err != nil || correlationID != "phase9-real" {
		t.Fatal("correlation not persisted in Outbox")
	}
	// Fresh Fx graph, HTTP listener and connection pool; database/keys persist.
	f.stop(t)
	f.start(t)
	afterRestart := f.wager(t, "provider-a", betBody, "bet-key", 200)
	if !afterRestart.IdempotentReplay || afterRestart.TransactionID != bet.TransactionID || afterRestart.ObservedBalance.Amount != "80.00" {
		t.Fatal("restart lost persisted replay")
	}
}

func TestIntegrationHTTPConcurrentThreeInstances(t *testing.T) {
	f := newAPIFixture(t)
	bases := []string{f.base}
	for range 2 {
		port := phase8Port(t)
		t.Setenv("HTTP_PORT", port)
		app := fx.New(fx.NopLogger, application.Module)
		if err := app.Start(f.ctx); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if err := app.Stop(ctx); err != nil {
				t.Error(err)
			}
		})
		bases = append(bases, "http://127.0.0.1:"+port)
	}
	w := f.wallet(t, "phase9-concurrent", "100.00")
	type outcome struct {
		status int
		result financial.WagerResult
		err    error
	}
	post := func(base string, body map[string]any, key string) outcome {
		raw, _ := json.Marshal(body)
		r, _ := http.NewRequestWithContext(f.ctx, "POST", base+"/wagering/transactions", bytes.NewReader(raw))
		r.Header.Set("Authorization", "Bearer "+f.tokens["provider-a"])
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Idempotency-Key", key)
		response, err := f.client.Do(r)
		if err != nil {
			return outcome{err: err}
		}
		defer response.Body.Close()
		var result financial.WagerResult
		err = json.NewDecoder(response.Body).Decode(&result)
		return outcome{status: response.StatusCode, result: result, err: err}
	}
	results := make(chan outcome, 50)
	start := make(chan struct{})
	body := wagerPayload(string(w.WalletID), "phase9-concurrent", "same-attempt", "BET", "20.00", "")
	for i := range 50 {
		go func(i int) { <-start; results <- post(bases[i%3], body, "same-key") }(i)
	}
	close(start)
	var transaction wager.TransactionID
	created := 0
	for range 50 {
		out := <-results
		if out.err != nil || out.status != 200 && out.status != 201 {
			t.Fatalf("concurrent HTTP status=%d err=%v", out.status, out.err)
		}
		if transaction == "" {
			transaction = out.result.TransactionID
		}
		if transaction != out.result.TransactionID {
			t.Fatal("same key created multiple transactions")
		}
		if out.status == 201 {
			created++
		} else if !out.result.IdempotentReplay {
			t.Fatal("missing replay flag")
		}
	}
	if created != 1 {
		t.Fatal("expected exactly one first request", created)
	}
	var balance int64
	var debits int
	if err := f.pool.QueryRow(f.ctx, `SELECT balance_cents,(SELECT count(*) FROM wallet_ledger_entries WHERE wallet_id=w.id AND direction='DEBIT') FROM wallets w WHERE id=$1`, string(w.WalletID)).Scan(&balance, &debits); err != nil || balance != 8000 || debits != 1 {
		t.Fatal("50 attempts duplicated debit", balance, debits, err)
	}
	w2 := f.wallet(t, "phase9-race80", "100.00")
	two := make(chan outcome, 2)
	barrier := make(chan struct{})
	for i := range 2 {
		go func(i int) {
			<-barrier
			two <- post(bases[i], wagerPayload(string(w2.WalletID), "phase9-race80", fmt.Sprintf("bet80-%d", i), "BET", "80.00", ""), fmt.Sprintf("key80-%d", i))
		}(i)
	}
	close(barrier)
	processed, rejected := 0, 0
	for range 2 {
		out := <-two
		if out.err != nil {
			t.Fatal(out.err)
		}
		if out.status == 201 && out.result.State == wager.StateProcessed {
			processed++
		} else if out.status == 422 && out.result.State == wager.StateRejected {
			rejected++
		} else {
			t.Fatal("unexpected race80 result", out.status)
		}
	}
	if processed != 1 || rejected != 1 {
		t.Fatal("two bets not serialized")
	}
	if err := f.pool.QueryRow(f.ctx, `SELECT balance_cents,(SELECT count(*) FROM wallet_ledger_entries WHERE wallet_id=w.id AND direction='DEBIT') FROM wallets w WHERE id=$1`, string(w2.WalletID)).Scan(&balance, &debits); err != nil || balance != 2000 || debits != 1 {
		t.Fatal("race80 ledger/balance", err, balance, debits)
	}
}

func TestIntegrationFinalHTTPCrossTransport(t *testing.T) {
	endpoint := os.Getenv("TEST_SQS_ENDPOINT")
	if endpoint == "" {
		t.Skip("set TEST_SQS_ENDPOINT for final HTTP + real SQS regression")
	}
	f := newAPIFixture(t)
	client, err := sqsadapter.NewClient(f.ctx, config.Config{AWSRegion: "us-east-1", SQSEndpoint: endpoint, AWSAccessKeyID: "test", AWSSecretAccessKey: "test"})
	if err != nil {
		t.Fatal(err)
	}
	queue, err := client.CreateQueue(f.ctx, &awssqs.CreateQueueInput{QueueName: aws.String(fmt.Sprintf("phase9-%d.fifo", time.Now().UnixNano())), Attributes: map[string]string{"FifoQueue": "true", "ContentBasedDeduplication": "false"}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := client.DeleteQueue(ctx, &awssqs.DeleteQueueInput{QueueUrl: queue.QueueUrl}); err != nil {
			t.Error(err)
		}
	})
	consumer, err := sqsadapter.NewConsumer(client, f.core, sqsadapter.Options{QueueURL: aws.ToString(queue.QueueUrl), ConsumerName: "phase9-cross", WaitSeconds: 1, VisibilitySeconds: 30, MaxMessages: 1, Concurrency: 1, ProcessingTimeout: 5 * time.Second, AckTimeout: 3 * time.Second, ReceiveRetryDelay: 50 * time.Millisecond}, slog.New(slog.NewJSONHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	w := f.wallet(t, "phase9-cross-player", "100.00")
	deliver := func(external string) {
		t.Helper()
		e := sqsadapter.Envelope{SchemaVersion: 1, MessageID: "delivery-" + external, ProviderID: "provider-a", ExternalTransactionID: external, PlayerID: "phase9-cross-player", WalletID: string(w.WalletID), Type: wager.TypeBet, Money: money.External{Amount: "20.00", Currency: "BRL"}, RoundID: "round", CorrelationID: "phase9-cross", CausationID: "cause-" + external, OccurredAt: time.Now().UTC()}
		input, err := sqsadapter.BuildSendInput(aws.ToString(queue.QueueUrl), e)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := client.SendMessage(f.ctx, input); err != nil {
			t.Fatal(err)
		}
		messages, err := client.ReceiveMessage(f.ctx, &awssqs.ReceiveMessageInput{QueueUrl: queue.QueueUrl, MaxNumberOfMessages: 1, WaitTimeSeconds: 1, MessageSystemAttributeNames: []types.MessageSystemAttributeName{types.MessageSystemAttributeNameAll}, MessageAttributeNames: []string{"All"}})
		if err != nil || len(messages.Messages) != 1 {
			t.Fatal("real SQS receive failed", err)
		}
		if err := consumer.Handle(f.ctx, messages.Messages[0]); err != nil {
			t.Fatal(err)
		}
	}
	f.wager(t, "provider-a", wagerPayload(string(w.WalletID), "phase9-cross-player", "http-first", "BET", "20.00", ""), "http-first-key", 201)
	deliver("http-first")
	deliver("sqs-first")
	f.request(t, "POST", "/wagering/transactions", "provider-a", wagerPayload(string(w.WalletID), "phase9-cross-player", "sqs-first", "BET", "20.00", ""), "sqs-first-key", 409)
	var balance int64
	var txs, ledger, inbox, outbox int
	if err := f.pool.QueryRow(f.ctx, `SELECT balance_cents,(SELECT count(*) FROM wager_transactions),(SELECT count(*) FROM wallet_ledger_entries),(SELECT count(*) FROM inbox_messages),(SELECT count(*) FROM outbox_events) FROM wallets WHERE id=$1`, string(w.WalletID)).Scan(&balance, &txs, &ledger, &inbox, &outbox); err != nil {
		t.Fatal(err)
	}
	if balance != 6000 || txs != 3 || ledger != 3 || inbox != 2 || outbox != 6 {
		t.Fatalf("cross transport duplicated effects: balance=%d tx=%d ledger=%d inbox=%d outbox=%d", balance, txs, ledger, inbox, outbox)
	}
}
