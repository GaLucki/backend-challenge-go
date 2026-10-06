# Phase 6: local operation and reproducible tests

Phase 7 note: the current `.env.example` also enables the Outbox publisher. See
[PHASE7_OPERATIONS.md](PHASE7_OPERATIONS.md) for the current migration and outgoing event queue.

Prerequisites: Docker Desktop/Compose, Go matching `go.mod`, and the `migrate` CLI used by the existing
project. Windows race tests additionally require CGO and a compatible C compiler.

## Start dependencies

```powershell
docker compose --env-file .env.example config
docker compose --env-file .env.example up --build -d --wait
docker compose --env-file .env.example ps
docker compose --env-file .env.example logs --tail 40 localstack
migrate -path migrations -database 'postgres://wagering:wagering@localhost:5432/wagering?sslmode=disable' up
```

The ready script provisions both FIFO queues automatically; no manual queue creation is needed.
Compose runs infrastructure, while the existing Go/Fx service runs on the host. `--env-file` makes
the command reproducible without overwriting an existing `.env`. Existing migrations 1–4 are unchanged;
phase 6 needs no migration because the Inbox already has the required primary key, hash and completion.

## Run the Go service

In a new PowerShell session, load the local example configuration and run the existing entrypoint:

```powershell
Get-Content .env.example | ForEach-Object {
    $phase6Line = $_.Trim()
    if ($phase6Line -and -not $phase6Line.StartsWith('#')) {
        $phase6Parts = $phase6Line.Split('=', 2)
        [Environment]::SetEnvironmentVariable($phase6Parts[0], $phase6Parts[1], 'Process')
    }
}
go run ./cmd/api
```

Health endpoints remain `/health/live` and `/health/ready`; the latter checks PostgreSQL. At startup,
the SQS hook verifies FIFO/redrive/DLQ configuration. Stop with Ctrl+C; Fx cancels polling and drains
active SQL work. No final financial HTTP endpoint or Outbox publisher is introduced by this phase.

## Verify queues and inspect DLQ

```powershell
docker compose --env-file .env.example exec localstack awslocal sqs list-queues
docker compose --env-file .env.example exec localstack awslocal sqs get-queue-attributes --queue-url http://localhost:4566/queue/us-east-1/000000000000/wager-transactions.fifo --attribute-names All
docker compose --env-file .env.example exec localstack awslocal sqs get-queue-attributes --queue-url http://localhost:4566/queue/us-east-1/000000000000/wager-transactions-dlq.fifo --attribute-names All
docker compose --env-file .env.example exec localstack awslocal sqs receive-message --queue-url http://localhost:4566/queue/us-east-1/000000000000/wager-transactions-dlq.fifo --wait-time-seconds 5 --visibility-timeout 10 --message-system-attribute-names All
```

These queue paths work with the configured dynamic LocalStack endpoint strategy. For another endpoint,
resolve URLs using `awslocal sqs get-queue-url --queue-name <name>` and use the returned URL.
Receiving a DLQ message temporarily hides it; the command does not delete or replay it.

`SQS_MAX_RECEIVE_COUNT=5` is the local provisioning redrive limit. Both permanent and prolonged transient
failures eventually reach DLQ; investigate and correct the cause before replay. Keep the original
envelope `messageId` and metadata when re-publishing an unchanged message. A corrected payload must
use a new delivery identity; external financial uniqueness still prevents reapplying a committed
operation. Automatic DLQ replay and Outbox publishing are outside phase 6.

## Tests

```powershell
$env:TEST_DATABASE_URL = 'postgres://wagering:wagering@localhost:5432/wagering?sslmode=disable'
$env:TEST_SQS_ENDPOINT = 'http://localhost:4566'
go test ./... -count=1
go vet ./...
$env:CGO_ENABLED = '1'
go test -race ./...
```

For the new integration scenarios only:

```powershell
go test ./internal/adapter/postgres -run IntegrationSQS -count=1 -v
go test ./internal/application -run IntegrationFxSQSLifecycle -count=1 -v
```

No build tags are required. Without the test variables, corresponding integration tests explicitly
skip; that is not infrastructure validation. The provisioned-queues test asserts local defaults
(redrive 5, visibility 90s, polling 20s), so use `.env.example` when reproducing the suite.
Temporary test queues use redrive 3 and visibility 10s; tests explicitly expire real receipts to
accelerate retry. Test schemas and queues are removed afterward, without purging the provisioned queues.
The DB user needs CREATE SCHEMA and visibility of its own lock waits in `pg_stat_activity`.

Stress and recovery assertions inspect balance, wallet version, ledger, wager transactions, Outbox
and Inbox. Three services use independently constructed pools and SDK clients. The stress uses 50
different AWS dedup IDs for one unchanged durable envelope, plus 50 concurrent processing attempts;
it does not rely on FIFO's temporary deduplication to pass. SQL triggers simulate precommit failures,
ACK omission simulates a committed crash, and a real invalid receipt simulates postcommit delete failure.

LocalStack is an emulator and its queue state here is ephemeral. These tests do not claim validation
against a deployed AWS account or a hard process kill. PostgreSQL financial effects remain durable.

Reference documentation: [AWS SDK Go v2 SQS](https://docs.aws.amazon.com/sdk-for-go/v2/developer-guide/go_sqs_code_examples.html),
[SQS delivery and FIFO semantics](https://aws.amazon.com/sqs/faqs/),
[LocalStack SQS](https://docs.localstack.cloud/aws/services/sqs/).
