package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awssqs "github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/junglegaming/backend-challenge-go/internal/domain/wager"
)

func TestIntegrationConsumerAbruptProcessDeathAndRestart(t *testing.T) {
	for _, boundary := range []string{"before-commit", "after-commit-before-delete"} {
		t.Run(boundary, func(t *testing.T) {
			f := newSQSFixture(t)
			w := createFinancialWallet(t, f.ctx, f.service, "kill-player", 10000)
			e := f.envelope(t, w.WalletID, "kill-player", "kill-bet", wager.TypeBet, 2000, "")
			var release func()
			if boundary == "before-commit" {
				// A disposable SQL trigger blocks at Outbox INSERT, after wallet,
				// transaction and ledger writes but before the financial commit.
				conn, err := f.pool.Acquire(f.ctx)
				must(t, err)
				lock := time.Now().UnixNano()
				_, err = conn.Exec(f.ctx, `SELECT pg_advisory_lock($1)`, lock)
				must(t, err)
				release = func() { _, _ = conn.Exec(context.Background(), `SELECT pg_advisory_unlock($1)`, lock); conn.Release() }
				defer func() {
					if release != nil {
						release()
					}
				}()
				_, err = f.pool.Exec(f.ctx, fmt.Sprintf(`CREATE FUNCTION final_block_commit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN PERFORM pg_advisory_xact_lock(%d); RETURN NEW; END $$; CREATE TRIGGER final_block_commit BEFORE INSERT ON outbox_events FOR EACH ROW EXECUTE FUNCTION final_block_commit();`, lock))
				must(t, err)
			}
			f.send(t, e, "")
			child := startAuditChild(t, f.ctx, f.pool)
			must(t, json.NewEncoder(child.input).Encode(auditProcessRequest{Mode: "consumer-kill", Queue: f.queue, Count: 1}))
			var received auditProcessReply
			must(t, child.output.Decode(&received))
			if received.Receipt == "" {
				t.Fatal("worker did not receive actual SQS delivery")
			}
			if boundary == "before-commit" {
				deadline := time.Now().Add(5 * time.Second)
				blocked := false
				for time.Now().Before(deadline) {
					must(t, f.pool.QueryRow(f.ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE application_name=$1 AND wait_event='advisory')`, fmt.Sprintf("final-audit-child-%d", child.pid)).Scan(&blocked))
					if blocked {
						break
					}
					time.Sleep(10 * time.Millisecond)
				}
				if !blocked {
					t.Fatal("worker never reached uncommitted financial boundary")
				}
			} else {
				var committed auditProcessReply
				must(t, child.output.Decode(&committed))
				if len(committed.Wagers) != 1 || committed.Wagers[0].State != wager.StateProcessed {
					t.Fatal("worker did not confirm financial commit")
				}
			}
			must(t, child.command.Process.Kill()) // actual OS termination, no Go cleanup
			if child.command.Wait() == nil {
				t.Fatal("worker exited normally rather than being killed")
			}
			t.Logf("killed independent process %d at %s", child.pid, boundary)
			if release != nil {
				release()
				release = nil
				_, err := f.pool.Exec(f.ctx, `DROP TRIGGER final_block_commit ON outbox_events; DROP FUNCTION final_block_commit()`)
				must(t, err)
				verifyWallet(t, f.ctx, f.pool, w.WalletID, 10000, 1)
				countRows(t, f.ctx, f.pool, "inbox_messages", 0)
			} else {
				verifyWallet(t, f.ctx, f.pool, w.WalletID, 8000, 2)
				countRows(t, f.ctx, f.pool, "inbox_messages", 1)
			}
			_, err := f.client.ChangeMessageVisibility(f.ctx, &awssqs.ChangeMessageVisibilityInput{QueueUrl: aws.String(f.queue), ReceiptHandle: aws.String(received.Receipt), VisibilityTimeout: 0})
			must(t, err)
			restarted := startAuditChild(t, f.ctx, f.pool)
			must(t, json.NewEncoder(restarted.input).Encode(auditProcessRequest{Mode: "consumer", Queue: f.queue, Count: 1}))
			_ = restarted.input.Close()
			var result auditProcessReply
			must(t, restarted.output.Decode(&result))
			must(t, restarted.command.Wait())
			if result.Received != 1 || result.PID == child.pid {
				t.Fatal("restart did not consume durable redelivery")
			}
			verifyWallet(t, f.ctx, f.pool, w.WalletID, 8000, 2)
			countRows(t, f.ctx, f.pool, "inbox_messages", 1)
			countRows(t, f.ctx, f.pool, "wallet_ledger_entries", 2)
			countRows(t, f.ctx, f.pool, "wager_transactions", 2)
			countRows(t, f.ctx, f.pool, "outbox_events", 4)
			f.empty(t, f.queue)
		})
	}
}
