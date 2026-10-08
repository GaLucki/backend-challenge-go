package application_test

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awssqs "github.com/aws/aws-sdk-go-v2/service/sqs"
	sqsadapter "github.com/junglegaming/backend-challenge-go/internal/adapter/sqs"
	"github.com/junglegaming/backend-challenge-go/internal/application/financial"
	"github.com/junglegaming/backend-challenge-go/internal/domain/wallet"
)

func TestIntegrationOriginalHTTPAndSQSContractReplay(t *testing.T) {
	f := newAPIFixture(t)
	queues := newObservationQueues(t, f.ctx)
	raw := f.request(t, "POST", "/wallets", "internal-service", map[string]any{"playerId": "original-player", "initialBalance": map[string]string{"amount": "100.00", "currency": "BRL"}}, "", 201)
	var opened struct {
		ID      string `json:"id"`
		Version int64  `json:"version"`
	}
	if json.Unmarshal(raw, &opened) != nil || opened.ID == "" || opened.Version != 1 {
		t.Fatal("original opening response missing", string(raw))
	}
	payload := map[string]any{"providerId": "provider-a", "externalTransactionId": "original-bet", "playerId": "original-player", "walletId": opened.ID, "gameId": "original-game", "roundId": "original-round", "kind": "BET", "money": map[string]string{"amount": "20.00", "currency": "BRL"}}
	raw = f.request(t, "POST", "/wagering/transactions", "provider-a", payload, "original-key", 201)
	var result struct {
		Status        string            `json:"status"`
		Balance       map[string]string `json:"balance"`
		TransactionID string            `json:"transactionId"`
	}
	if json.Unmarshal(raw, &result) != nil || result.Status != "PROCESSED" || result.Balance["amount"] != "80.00" {
		t.Fatal("original wager response missing", string(raw))
	}
	var game string
	if err := f.pool.QueryRow(f.ctx, `SELECT game_id FROM wager_transactions WHERE transaction_id=$1`, result.TransactionID).Scan(&game); err != nil || game != "original-game" {
		t.Fatal("game not durably preserved", game, err)
	}
	f.request(t, "GET", "/wagering/transactions/"+result.TransactionID, "provider-a", nil, "", 200)
	data := map[string]any{}
	for key, value := range payload {
		data[key] = value
	}
	data["idempotencyKey"] = "original-key"
	envelope := map[string]any{"messageId": "original-sqs-delivery", "type": "WagerTransactionRequested", "occurredAt": time.Now().UTC(), "data": data}
	body, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	_, err = queues.client.SendMessage(f.ctx, &awssqs.SendMessageInput{QueueUrl: aws.String(queues.manual), MessageBody: aws.String(string(body)), MessageGroupId: aws.String(sqsadapter.MessageGroupID(opened.ID)), MessageDeduplicationId: aws.String("original-delivery")})
	if err != nil {
		t.Fatal(err)
	}
	consumer, err := sqsadapter.NewConsumer(queues.client, f.core, sqsadapter.Options{QueueURL: queues.manual, ConsumerName: "original-consumer", WaitSeconds: 1, VisibilitySeconds: 30, MaxMessages: 1, Concurrency: 1, ProcessingTimeout: 5 * time.Second, AckTimeout: 2 * time.Second, ReceiveRetryDelay: time.Second}, slog.New(slog.NewJSONHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	messages, err := consumer.Receive(f.ctx)
	if err != nil || len(messages) != 1 {
		t.Fatal("real original envelope not received", err)
	}
	replay, err := consumer.Process(f.ctx, messages[0])
	if err != nil || replay.Outcome != "replay" || string(replay.Financial.TransactionID) != result.TransactionID || replay.Financial.ObservedBalance.Amount != "80.00" {
		t.Fatal(replay, err)
	}
	if err := consumer.Handle(f.ctx, messages[0]); err != nil {
		t.Fatal(err)
	}
	payload["gameId"] = "changed-game"
	f.request(t, "POST", "/wagering/transactions", "provider-a", payload, "original-key", 409)
	payload["gameId"] = "original-game"
	f.request(t, "POST", "/wagering/transactions", "provider-b", payload, "original-key", 403)
	raw = f.request(t, "POST", "/wallets/"+opened.ID+"/reconciliation", "internal-service", nil, "", 200)
	var audit struct {
		Consistent        bool
		CheckedEntries    int
		Difference        map[string]string
		StoredBalance     map[string]string
		CalculatedBalance map[string]string
	}
	if json.Unmarshal(raw, &audit) != nil || !audit.Consistent || audit.CheckedEntries != 2 || audit.Difference["amount"] != "0.00" || audit.StoredBalance["amount"] != "80.00" || audit.CalculatedBalance["amount"] != "80.00" {
		t.Fatal("original reconciliation fields missing", string(raw))
	}
	var debitCount int
	if err := f.pool.QueryRow(f.ctx, `SELECT count(*) FROM wallet_ledger_entries WHERE wallet_id=$1 AND direction='DEBIT'`, opened.ID).Scan(&debitCount); err != nil || debitCount != 1 {
		t.Fatal("duplicate debit", debitCount, err)
	}
	// Original SQS first -> authenticated HTTP must replay the same durable key.
	data["externalTransactionId"], data["idempotencyKey"] = "sqs-first", "sqs-first-key"
	envelope["messageId"] = "sqs-first-message"
	body, _ = json.Marshal(envelope)
	_, err = queues.client.SendMessage(f.ctx, &awssqs.SendMessageInput{QueueUrl: aws.String(queues.manual), MessageBody: aws.String(string(body)), MessageGroupId: aws.String(sqsadapter.MessageGroupID(opened.ID)), MessageDeduplicationId: aws.String("sqs-first")})
	if err != nil {
		t.Fatal(err)
	}
	messages, err = consumer.Receive(f.ctx)
	if err != nil || len(messages) != 1 {
		t.Fatal(err)
	}
	if err := consumer.Handle(f.ctx, messages[0]); err != nil {
		t.Fatal(err)
	}
	payload["externalTransactionId"] = "sqs-first"
	httpReplay := f.wager(t, "provider-a", payload, "sqs-first-key", http.StatusOK)
	if !httpReplay.IdempotentReplay || httpReplay.ObservedBalance.Amount != "60.00" {
		t.Fatal(httpReplay)
	}
	w, err := f.core.CreateWallet(f.ctx, financial.CreateWalletInput{PlayerID: wallet.PlayerID("original-zero"), Currency: "BRL"})
	if err == nil || w.WalletID != "" {
		t.Fatal("uninitialized money accepted")
	}
}
