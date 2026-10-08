package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/jackc/pgx/v5/pgxpool"
	sqsadapter "github.com/junglegaming/backend-challenge-go/internal/adapter/sqs"
	"github.com/junglegaming/backend-challenge-go/internal/application/financial"
	"github.com/junglegaming/backend-challenge-go/internal/application/outbox"
	"github.com/junglegaming/backend-challenge-go/internal/config"
	"github.com/junglegaming/backend-challenge-go/internal/domain/money"
	"github.com/junglegaming/backend-challenge-go/internal/domain/wager"
	"github.com/junglegaming/backend-challenge-go/internal/domain/wallet"
)

type auditJob struct {
	Wallet                           wallet.ID
	Player, External, Key, Reference string
	Kind                             wager.Type
	Cents                            int64
}
type auditProcessRequest struct {
	Mode  string
	Jobs  []auditJob
	Queue string
	Count int
}
type auditProcessReply struct {
	PID      int
	Wagers   []financial.WagerResult
	Received int
	Receipt  string
}
type auditChild struct {
	command *exec.Cmd
	input   io.WriteCloser
	output  *json.Decoder
	errors  bytes.Buffer
	pid     int
}

// The same compiled test executable is launched as a real OS process. It has
// its own Go heap, SQL pool, SDK client and service; no parent's objects escape.
func startAuditChild(t *testing.T, ctx context.Context, pool *pgxpool.Pool) *auditChild {
	t.Helper()
	c := &auditChild{}
	c.command = exec.CommandContext(ctx, os.Args[0], "-test.run=^TestFinalAuditProcessHelper$")
	c.command.Env = append(os.Environ(), "FINAL_AUDIT_CHILD=1", "FINAL_AUDIT_DSN="+os.Getenv("TEST_DATABASE_URL"), "FINAL_AUDIT_SCHEMA="+pool.Config().ConnConfig.RuntimeParams["search_path"])
	var err error
	c.input, err = c.command.StdinPipe()
	must(t, err)
	output, err := c.command.StdoutPipe()
	must(t, err)
	c.output = json.NewDecoder(output)
	c.command.Stderr = &c.errors
	must(t, c.command.Start())
	t.Cleanup(func() {
		_ = c.input.Close()
		if c.command.ProcessState == nil {
			_ = c.command.Process.Kill()
			_ = c.command.Wait()
		}
	})
	var ready auditProcessReply
	must(t, c.output.Decode(&ready))
	c.pid = ready.PID
	if c.pid == os.Getpid() || c.pid == 0 {
		t.Fatal("worker was not an independent process")
	}
	return c
}

func runAuditProcesses(t *testing.T, ctx context.Context, pool *pgxpool.Pool, requests [3]auditProcessRequest) []auditProcessReply {
	t.Helper()
	children := [3]*auditChild{}
	pids := map[int]bool{}
	for i := range children {
		children[i] = startAuditChild(t, ctx, pool)
		if pids[children[i].pid] {
			t.Fatal("duplicate process identity")
		}
		pids[children[i].pid] = true
	}
	var wg sync.WaitGroup
	errors := make(chan error, 3)
	replies := make([]auditProcessReply, 3)
	for i, child := range children {
		wg.Add(1)
		go func(i int, child *auditChild) {
			defer wg.Done()
			if err := json.NewEncoder(child.input).Encode(requests[i]); err != nil {
				errors <- err
				return
			}
			_ = child.input.Close()
			if err := child.output.Decode(&replies[i]); err != nil {
				errors <- fmt.Errorf("process %d did not return results: %w", child.pid, err)
				return
			}
			if err := child.command.Wait(); err != nil {
				errors <- fmt.Errorf("process %d failed: %w", child.pid, err)
			}
		}(i, child)
	}
	wg.Wait()
	close(errors)
	for err := range errors {
		t.Error(err)
	}
	if t.Failed() {
		t.FailNow()
	}
	t.Logf("independent worker PIDs: %d, %d, %d", children[0].pid, children[1].pid, children[2].pid)
	return replies
}

func TestFinalAuditProcessHelper(t *testing.T) {
	if os.Getenv("FINAL_AUDIT_CHILD") != "1" {
		t.Skip("subprocess helper; exercised by parent integration tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cfg, err := pgxpool.ParseConfig(os.Getenv("FINAL_AUDIT_DSN"))
	must(t, err)
	cfg.ConnConfig.RuntimeParams["search_path"] = os.Getenv("FINAL_AUDIT_SCHEMA")
	cfg.ConnConfig.RuntimeParams["application_name"] = fmt.Sprintf("final-audit-child-%d", os.Getpid())
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	must(t, err)
	defer pool.Close()
	must(t, pool.Ping(ctx))
	encoder := json.NewEncoder(os.Stdout)
	must(t, encoder.Encode(auditProcessReply{PID: os.Getpid()}))
	var request auditProcessRequest
	must(t, json.NewDecoder(os.Stdin).Decode(&request))
	core := financial.NewService(NewUnitOfWork(pool))
	reply := auditProcessReply{PID: os.Getpid()}
	if request.Mode == "consumer" || request.Mode == "consumer-kill" {
		client, err := sqsadapter.NewClient(ctx, config.Config{AppEnv: "test", AWSRegion: "us-east-1", AWSAccessKeyID: "test", AWSSecretAccessKey: "test", SQSEndpoint: os.Getenv("TEST_SQS_ENDPOINT")})
		must(t, err)
		consumer, err := sqsadapter.NewConsumer(client, core, sqsadapter.Options{QueueURL: request.Queue, ConsumerName: "final-process-consumer", WaitSeconds: 1, VisibilitySeconds: 30, MaxMessages: 1, Concurrency: 1, ProcessingTimeout: 5 * time.Second, AckTimeout: 2 * time.Second, ReceiveRetryDelay: time.Second}, slog.New(slog.NewJSONHandler(io.Discard, nil)))
		must(t, err)
		for reply.Received < request.Count {
			messages, err := consumer.Receive(ctx)
			must(t, err)
			for _, message := range messages {
				if request.Mode == "consumer-kill" {
					must(t, encoder.Encode(auditProcessReply{PID: os.Getpid(), Receipt: aws.ToString(message.ReceiptHandle)}))
				}
				result, err := consumer.Process(ctx, message)
				must(t, err)
				if request.Mode == "consumer-kill" {
					must(t, encoder.Encode(auditProcessReply{PID: os.Getpid(), Wagers: []financial.WagerResult{result.Financial}}))
					<-ctx.Done()
					t.Fatal("parent did not terminate worker before deadline")
				}
				must(t, consumer.Handle(ctx, message))
				reply.Wagers = append(reply.Wagers, result.Financial)
				reply.Received++
			}
		}
	} else if request.Mode == "publisher" {
		client, err := sqsadapter.NewClient(ctx, config.Config{AppEnv: "test", AWSRegion: "us-east-1", AWSAccessKeyID: "test", AWSSecretAccessKey: "test", SQSEndpoint: os.Getenv("TEST_SQS_ENDPOINT")})
		must(t, err)
		publisher, err := outbox.NewPublisher(NewPublicationStore(pool), sqsadapter.NewEventSender(client, request.Queue), outbox.Options{PublisherID: fmt.Sprint(os.Getpid()), PollInterval: 10 * time.Millisecond, BatchSize: 10, BaseRetryDelay: time.Second, MaxRetryDelay: time.Minute, ClaimDuration: 30 * time.Second, PublishTimeout: 5 * time.Second, StoreTimeout: 3 * time.Second}, slog.New(slog.NewJSONHandler(io.Discard, nil)))
		must(t, err)
		for {
			_, err := publisher.RunOnce(ctx, ctx)
			must(t, err)
			var remaining int
			must(t, pool.QueryRow(ctx, `SELECT count(*) FROM outbox_events WHERE published_at IS NULL`).Scan(&remaining))
			if remaining == 0 {
				break
			}
			select {
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			case <-time.After(10 * time.Millisecond):
			}
		}
	} else {
		for _, job := range request.Jobs {
			amount, err := money.New(job.Cents, "BRL")
			must(t, err)
			result, err := core.ProcessHTTPWager(ctx, financial.HTTPWagerInput{WagerInput: financial.WagerInput{ProviderID: "provider", ExternalTransactionID: wager.ExternalTransactionID(job.External), PlayerID: wager.PlayerID(job.Player), WalletID: job.Wallet, Type: job.Kind, Amount: amount, RoundID: "round", GameID: "process-game", ReferenceExternalTransactionID: wager.ExternalTransactionID(job.Reference), CorrelationID: "process-audit"}, IdempotencyKey: job.Key})
			must(t, err)
			reply.Wagers = append(reply.Wagers, result)
		}
	}
	must(t, encoder.Encode(reply))
}

func TestIntegrationThreeProcessesFinancialConcurrency(t *testing.T) {
	pool, ctx := phase7Pool(t)
	core := financial.NewService(NewUnitOfWork(pool))
	w := createFinancialWallet(t, ctx, core, "process-player", 10000)
	other := createFinancialWallet(t, ctx, core, "independent-player", 10000)
	requests := [3]auditProcessRequest{}
	for i := range requests {
		job := auditJob{Wallet: w.WalletID, Player: "process-player", External: fmt.Sprintf("bet-%d", i), Key: fmt.Sprintf("bet-key-%d", i), Kind: wager.TypeBet, Cents: 8000}
		if i == 2 {
			job.Wallet, job.Player, job.Cents = other.WalletID, "independent-player", 2000
		}
		requests[i].Jobs = []auditJob{job}
	}
	replies := runAuditProcesses(t, ctx, pool, requests)
	processed, rejected := 0, 0
	for _, reply := range replies[:2] {
		for _, result := range reply.Wagers {
			if result.State == wager.StateProcessed {
				processed++
			}
			if result.State == wager.StateRejected && result.FailureCode == financial.FailureInsufficientFunds {
				rejected++
			}
		}
	}
	if processed != 1 || rejected != 1 {
		t.Fatal("two 80 bets did not serialize safely", processed, rejected)
	}
	verifyWallet(t, ctx, pool, w.WalletID, 2000, 2)
	verifyWallet(t, ctx, pool, other.WalletID, 8000, 2)
	var debits int
	must(t, pool.QueryRow(ctx, `SELECT count(*) FROM wallet_ledger_entries WHERE wallet_id=$1 AND direction='DEBIT'`, string(w.WalletID)).Scan(&debits))
	if debits != 1 {
		t.Fatal("double spending", debits)
	}
	duplicateWallet := createFinancialWallet(t, ctx, core, "duplicate-player", 10000)
	requests = [3]auditProcessRequest{}
	for i := 0; i < 50; i++ {
		requests[i%3].Jobs = append(requests[i%3].Jobs, auditJob{Wallet: duplicateWallet.WalletID, Player: "duplicate-player", External: "duplicate-bet", Key: "duplicate-key", Kind: wager.TypeBet, Cents: 2000})
	}
	replies = runAuditProcesses(t, ctx, pool, requests)
	replays := 0
	for _, reply := range replies {
		for _, result := range reply.Wagers {
			if result.IdempotentReplay {
				replays++
			}
		}
	}
	if replays != 49 {
		t.Fatal("50 attempts did not produce one unique result", replays)
	}
	verifyWallet(t, ctx, pool, duplicateWallet.WalletID, 8000, 2)
	must(t, pool.QueryRow(ctx, `SELECT count(*) FROM wallet_ledger_entries WHERE wallet_id=$1 AND direction='DEBIT'`, string(duplicateWallet.WalletID)).Scan(&debits))
	if debits != 1 {
		t.Fatal("duplicate ledger", debits)
	}
	requests = [3]auditProcessRequest{}
	for i := range requests {
		kind := wager.TypeRefund
		if i == 1 {
			kind = wager.TypeRollback
		}
		requests[i].Jobs = []auditJob{{Wallet: duplicateWallet.WalletID, Player: "duplicate-player", External: fmt.Sprintf("reverse-%d", i), Key: fmt.Sprintf("reverse-key-%d", i), Kind: kind, Cents: 2000, Reference: "duplicate-bet"}}
	}
	replies = runAuditProcesses(t, ctx, pool, requests)
	processed, rejected = 0, 0
	for _, reply := range replies {
		for _, result := range reply.Wagers {
			if result.State == wager.StateProcessed {
				processed++
			}
			if result.FailureCode == financial.FailureAlreadyReversed {
				rejected++
			}
		}
	}
	if processed != 1 || rejected != 2 {
		t.Fatal("duplicate cross-type reversal", processed, rejected)
	}
	verifyWallet(t, ctx, pool, duplicateWallet.WalletID, 10000, 3)
}

func TestIntegrationThreeConsumerProcessesFiftyRealDuplicates(t *testing.T) {
	f := newSQSFixture(t)
	w := createFinancialWallet(t, f.ctx, f.service, "process-player", 10000)
	e := f.envelope(t, w.WalletID, "process-player", "process-bet", wager.TypeBet, 2000, "")
	for i := 0; i < 50; i++ {
		input, err := sqsadapter.BuildSendInput(f.queue, e)
		must(t, err)
		input.MessageDeduplicationId = aws.String(fmt.Sprintf("actual-duplicate-%d", i))
		_, err = f.client.SendMessage(f.ctx, input)
		must(t, err)
	}
	requests := [3]auditProcessRequest{}
	for i := range requests {
		requests[i] = auditProcessRequest{Mode: "consumer", Queue: f.queue, Count: 16}
		if i < 2 {
			requests[i].Count = 17
		}
	}
	replies := runAuditProcesses(t, f.ctx, f.pool, requests)
	received := 0
	for _, reply := range replies {
		received += reply.Received
	}
	if received != 50 {
		t.Fatal("FIFO dedup hid required application duplicates", received)
	}
	verifyWallet(t, f.ctx, f.pool, wallet.ID(e.WalletID), 8000, 2)
	countRows(t, f.ctx, f.pool, "inbox_messages", 1)
	countRows(t, f.ctx, f.pool, "wager_transactions", 2)
	countRows(t, f.ctx, f.pool, "wallet_ledger_entries", 2)
	countRows(t, f.ctx, f.pool, "outbox_events", 4)
}

func TestIntegrationThreePublisherProcessesHundredEvents(t *testing.T) {
	f := newOutboxFixture(t)
	core := financial.NewService(NewUnitOfWork(f.pool))
	w := createFinancialWallet(t, f.ctx, core, "publisher-player", 0)
	for i := 0; i < 50; i++ {
		runFinancial(t, f.ctx, core, financialInput(t, w.WalletID, "publisher-player", fmt.Sprintf("win-%d", i), fmt.Sprintf("win-key-%d", i), wager.TypeWin, 100))
	}
	countRows(t, f.ctx, f.pool, "outbox_events", 100)
	requests := [3]auditProcessRequest{}
	for i := range requests {
		requests[i] = auditProcessRequest{Mode: "publisher", Queue: f.queue}
	}
	runAuditProcesses(t, f.ctx, f.pool, requests)
	messages := f.receive(t, 100)
	seen := map[string]bool{}
	for _, message := range messages {
		var e financial.EventEnvelope
		must(t, json.Unmarshal([]byte(aws.ToString(message.Body)), &e))
		if seen[e.EventID] {
			t.Fatal("unexpected duplicate in successful publication fixture")
		}
		seen[e.EventID] = true
	}
	if len(seen) != 100 {
		t.Fatal("events lost", len(seen))
	}
	var remaining int
	must(t, f.pool.QueryRow(f.ctx, `SELECT count(*) FROM outbox_events WHERE published_at IS NULL`).Scan(&remaining))
	if remaining != 0 {
		t.Fatal("publication backlog not drained", remaining)
	}
}
