# Architecture

## Objective

This service will process distributed wagering operations with strong financial guarantees.
The current foundation covers process composition, HTTP health probes, structured logging,
correlation IDs, graceful shutdown, and PostgreSQL persistence infrastructure.

The financial domain (`Money`, `Wallet`, `WagerTransaction`) is implemented in-process and remains
independent of persistence and transport. Phase 3 adds financial persistence, ledger, inbox/outbox
storage and a SQL Unit of Work. Phase 4 implements CreateWallet, internal OPENING, BET/WIN/LOSS,
durable HTTP idempotency and outbox event creation. Phase 5 adds REFUND/ROLLBACK and a persistent
pending-reference worker. Phase 6 adds the incoming SQS FIFO transport, transactional Inbox,
LocalStack provisioning and crash recovery. Phase 7 publishes committed Outbox envelopes through
durable leases to a separate events FIFO. OIDC and wagering HTTP APIs remain for later phases.

## Package structure

| Path | Responsibility |
| --- | --- |
| `cmd/api` | Process entrypoint; constructs and runs the Fx application |
| `internal/config` | Environment-based configuration loading and validation |
| `internal/application` | Fx module composition for the process |
| `internal/application/financial` | Transport-independent financial use cases, results, errors, canonical hash and event envelopes |
| `internal/application/pending_worker.go` | Financial policy/worker construction and Fx lifecycle hooks |
| `internal/adapter/http` | HTTP server, health endpoints, error envelope, middleware |
| `internal/adapter/postgres` | pgxpool lifecycle, SQL repositories, row mappings and Unit of Work |
| `internal/adapter/sqs` | Versioned incoming envelope, AWS SDK v2 client, FIFO lanes and commit-before-delete consumer |
| `internal/application/sqs_consumer.go` | SQS startup validation, polling and drain through Fx |
| `internal/application/outbox` | Transport-independent publication orchestration, bounded backoff and batch drain |
| `internal/application/outbox_publisher.go` | Publisher construction, queue separation validation and Fx lifecycle |
| `infra/localstack` | Pinned emulator image and automatic FIFO/DLQ provisioning |
| `internal/ports` | Persistence interfaces, storage records and stable infrastructure errors |
| `internal/domain/money` | Immutable monetary value object (int64 cents + currency) |
| `internal/domain/wallet` | Wallet aggregate (credit/debit, version, rehydration) |
| `internal/domain/wager` | Wager transaction entity and explicit state machine |
| `internal/observability` | JSON logger and context helpers for request identifiers |
| `migrations/` | Versioned SQL migrations (`up` / `down`) |
| `docs/` | Technical decisions and architecture notes |

Persistence ports depend on domain types; domain packages do not depend on ports or adapters.

## Role of Uber Fx

Uber Fx owns dependency injection and process lifecycle:

- `fx.Provide` registers constructors (config, logger, postgres pool, HTTP handler, readiness flag)
- `fx.Invoke` registers the HTTP server lifecycle hooks
- `fx.Lifecycle` starts dependencies on `OnStart` and releases them on `OnStop`
- SIGINT/SIGTERM are handled by `fx.App.Run()`, which triggers ordered shutdown

Domain logic must remain independent of Fx. The `application` package is the composition root.

## Startup flow

1. `main` creates `fx.New(application.Module)` and calls `Run()`
2. Config is loaded from environment variables and validated
3. JSON logger is created from `LOG_LEVEL`
4. PostgreSQL pool is created from `DATABASE_URL` and pinged on start
5. HTTP routes and middleware are assembled
6. Lifecycle `OnStart` binds the TCP listener, serves HTTP, and marks the process initialized
7. The pending-reference worker starts; when SQS is enabled, queues are validated and polling begins
8. When enabled, the Outbox publisher validates the separate event destination and starts polling

The application does **not** run migrations on startup. Schema changes are applied by the
deploy/infrastructure process before the service starts.

## Lifecycle and shutdown

On SIGINT/SIGTERM:

1. Fx begins stop hooks in reverse dependency order
2. Outbox polling stops and active sends drain, followed by SQS and the pending-reference worker
3. HTTP readiness is marked false and in-flight requests drain within `SHUTDOWN_TIMEOUT`
4. PostgreSQL pool is closed after its dependents stop

All workers stop before the shared PostgreSQL pool closes.

## PostgreSQL

PostgreSQL is the persistent source of truth.

| Decision | Choice |
| --- | --- |
| Driver | `pgx` / `pgxpool` |
| SQL style | Explicit SQL (no ORM) |
| Connection source | `DATABASE_URL` |
| Pool ownership | Uber Fx lifecycle (`OnStart` ping, `OnStop` close) |
| Global state | None — pool is injected by Fx |

Pool settings:

- `DB_MAX_CONNS`
- `DB_MIN_CONNS`
- `DB_MAX_CONN_LIFETIME`
- `DB_MAX_CONN_IDLE_TIME`
- `DB_CONNECT_TIMEOUT`
- `DB_HEALTH_TIMEOUT`

## Migrations

Tool: [golang-migrate](https://github.com/golang-migrate/migrate)

Files live in `migrations/` with paired `up` / `down` scripts.

Phase 1 includes the technical bootstrap table (`app_metadata`). Migration
`000002_financial_persistence` adds the five financial tables with paired UP/DOWN scripts.
`000003_wager_idempotency` adds durable claims and original results without changing migration 2.
`000004_reversals_pending_references` adds reversal reservations and pending retry metadata;
migrations 1, 2 and 3 are preserved unchanged.

### Install CLI

```sh
go install -tags 'postgres' github.com/golang-migrate/migrate/v4/cmd/migrate@v4.18.3
```

Ensure `$GOPATH/bin` (or `$HOME/go/bin`) is on `PATH`.

### Apply migrations

```sh
migrate -path migrations -database "$DATABASE_URL" up
```

### Rollback last migration

```sh
migrate -path migrations -database "$DATABASE_URL" down 1
```

### Check version / status

```sh
migrate -path migrations -database "$DATABASE_URL" version
```

Makefile helpers:

```sh
make postgres-up
make migrate-up
make migrate-down
make migrate-version
```

## Health endpoints

| Endpoint | Meaning |
| --- | --- |
| `GET /health/live` | Process is alive; does **not** depend on PostgreSQL |
| `GET /health/ready` | Process initialized **and** PostgreSQL ping succeeds within timeout |

Readiness failure response:

```json
{
  "error": {
    "code": "NOT_READY",
    "message": "database unavailable"
  }
}
```

## Local PostgreSQL with Docker Compose

Phase 1 compose stack includes **only** PostgreSQL. Keycloak, LocalStack, SQS, and the app
container will be added later.

```sh
docker compose --env-file .env.example config
docker compose --env-file .env.example up -d
```

Copy `.env.example` to `.env` for local overrides if desired. Do not commit real secrets.

## Configuration

| Variable | Required | Default | Notes |
| --- | --- | --- | --- |
| `APP_ENV` | yes* | `development` | Empty value is rejected |
| `HTTP_PORT` | yes* | `8080` | Must be `1..65535` |
| `LOG_LEVEL` | yes* | `info` | `debug`, `info`, `warn`, `error` |
| `SHUTDOWN_TIMEOUT` | no | `10s` | Go duration string |
| `DATABASE_URL` | yes | — | `postgres://` or `postgresql://` |
| `DB_MAX_CONNS` | no | `10` | Pool max connections |
| `DB_MIN_CONNS` | no | `1` | Pool min connections |
| `DB_MAX_CONN_LIFETIME` | no | `1h` | Max connection lifetime |
| `DB_MAX_CONN_IDLE_TIME` | no | `30m` | Max idle time |
| `DB_CONNECT_TIMEOUT` | no | `5s` | Connect/ping on startup |
| `DB_HEALTH_TIMEOUT` | no | `2s` | Readiness ping timeout |
| `POSTGRES_USER` | compose | — | Docker Compose only |
| `POSTGRES_PASSWORD` | compose | — | Docker Compose only |
| `POSTGRES_DB` | compose | — | Docker Compose only |
| `POSTGRES_PORT` | compose | `5432` | Host port mapping |
| `KEYCLOAK_URL` | no | — | Loaded, unused in phase 1 |
| `SQS_ENDPOINT` | no | — | Loaded, unused in phase 1 |
| `AWS_REGION` | no | — | Loaded, unused in phase 1 |
| `AWS_ACCESS_KEY_ID` | no | — | Loaded, unused in phase 1 |
| `AWS_SECRET_ACCESS_KEY` | no | — | Loaded, unused in phase 1 |

\* Required after defaults are applied; explicitly empty values fail validation.

## Local run

```sh
docker compose --env-file .env.example up -d
migrate -path migrations -database "postgres://wagering:wagering@localhost:5432/wagering?sslmode=disable" up
set DATABASE_URL=postgres://wagering:wagering@localhost:5432/wagering?sslmode=disable
go run ./cmd/api
```

```sh
go test ./...
go vet ./...
```

## Financial domain

The domain layer has no imports of PostgreSQL, pgx, HTTP, SQS, AWS, Keycloak, or Uber Fx.

### Money

- Representation: `int64` cents + ISO 4217 currency (`BRL`, `USD`, `EUR`, ...)
- Example: `2500` + `BRL` => `25.00 BRL`
- Public API is immutable; operations return new values
- Arithmetic requires matching currencies and detects overflow/underflow
- External form: `{"amount":"25.00","currency":"BRL"}` via `Money.External()`
- `ParseDecimal` converts decimal strings to cents without floating point
- External parsing rejects negatives, scientific notation, NaN/Infinity, and more than 2 decimals

### Wallet

- Aggregate fields: ID, player ID, currency, balance, version
- Version starts at `1` and increments by exactly `1` only when balance changes
- `Credit` / `Debit` require a positive amount in the wallet currency
- Balance never becomes negative; failed operations leave state unchanged
- `Rehydrate` rebuilds persisted state without executing credit/debit

### WagerTransaction

Types: `BET`, `WIN`, `LOSS`, `REFUND`, `ROLLBACK`, and internal-only `OPENING`.

States:

| State | Terminal |
| --- | --- |
| `PENDING` | no |
| `PENDING_REFERENCE` | no |
| `PROCESSED` | yes |
| `REJECTED` | yes |
| `FAILED` | yes |

Allowed transitions:

- `PENDING` -> `PROCESSED` | `REJECTED` | `FAILED` | `PENDING_REFERENCE`
- `PENDING_REFERENCE` -> `PROCESSED` | `REJECTED` | `FAILED`

Terminal states are immutable. External constructors reject `OPENING`.

### Domain vs infrastructure

| Concern | Location |
| --- | --- |
| Invariants and state transitions | `internal/domain/*` |
| HTTP, PostgreSQL, Fx, messaging | `internal/adapter/*`, `internal/application` |

### Financial persistence (phase 3)

IDs remain `TEXT`, matching existing domain identifiers without introducing a UUID requirement.
Amounts and balances use exact `BIGINT` cents. Currency is structurally validated as three
uppercase ASCII letters, matching the domain; this does not validate an ISO registry.
All timestamps use `TIMESTAMPTZ` (PostgreSQL microsecond precision).

| Table | Main guarantees |
| --- | --- |
| `wallets` | Unique player/currency; balance >= 0; version >= 1 |
| `wager_transactions` | Unique provider/external ID; nonnegative amount; allowed types/states; wallet FK and matching owner/currency |
| `wallet_ledger_entries` | Unique wallet/transaction; positive amount; nonnegative balances; version >= 1; matching wallet/transaction FK; exact debit/credit balance arithmetic |
| `inbox_messages` | Composite primary key consumer/message; lowercase hex SHA-256 payload hash |
| `outbox_events` | Stable caller-supplied event ID primary key; JSONB payload; retry count >= 0; partial index on next attempt/event ID for unpublished rows |

External transactions require provider/external/round IDs. Internal `OPENING` rows store those
fields as SQL NULL, so multiple openings do not collide on the external uniqueness constraint.
Optional domain strings map to NULL on write and back to empty strings on read.
Wallet and transaction mappings call `money.New`, `wallet.Rehydrate` and `wager.Rehydrate`.
The latter is a phase 3 addition: it uses existing constructors for validation and restores
persisted state without replaying business operations or changing phase 2 transition rules.

Ports expose `WalletRepository`, `WagerTransactionRepository`, `LedgerRepository`,
`InboxRepository`, `OutboxRepository`, and `UnitOfWork`. Fx registers these interfaces and the
repository bundle, reusing the single existing lifecycle-owned pool.
SQLSTATE-based error mapping exposes stable not-found, unique, constraint, transaction and
persistence errors; stale conditional updates return conflict. Context cancellation is preserved.
Raw PostgreSQL messages do not cross the adapter boundary. Retrying transactions is the caller's
future responsibility; the Unit of Work does not automatically replay callbacks.

### Unit of Work and wallet concurrency

`WithinTransaction(ctx, func(ports.Repositories) error)` begins one `pgx.Tx`, binds every repository
in the supplied bundle to that same transaction, commits on success and rolls back on callback
error or panic. Deferred rollback also handles failed commits, using a separate bounded cleanup
context if the caller was canceled. Panics propagate after cleanup. A failed commit due to a lost
connection can have an ambiguous outcome; callers can retry the same HTTP idempotency key to
recover the persisted result if the transaction committed.

```go
err := uow.WithinTransaction(ctx, func(r ports.Repositories) error {
    w, err := r.Wallets.GetForUpdate(ctx, walletID)
    if err != nil { return err }
    // Future use case applies domain operations and saves through this same bundle.
    _ = w
    return nil
})
```

`GetForUpdate` issues `SELECT ... FOR UPDATE`, holding a row lock until commit/rollback.
It requires a transaction-scoped repository; pool-scoped lock calls return transaction-required.
There is no global or in-memory lock: different wallet rows can proceed independently across
service instances. Wallet `Update` also requires the previously observed version and advances
only to a higher version, preventing stale writes. It saves balance/version without changing
ownership or currency. Wager `Update` writes only processing state, failure code and updated time,
with an expected-state condition and protection against overwriting terminal records.

Financial use cases must use the callback's repositories for **all** wallet, transaction,
ledger, inbox and outbox writes to achieve atomicity. Do not use pool repositories from inside the
callback. Transaction repositories must not escape the callback or be shared between goroutines.
Standalone reads/inserts are supported for setup and inspection; they do not provide cross-table
atomicity. Nested Unit of Work calls are not savepoints and must not be used for one operation.

### Append-only ledger, inbox and outbox

`reject_ledger_mutation()` is a statement-level trigger function that raises SQLSTATE `23514`
before UPDATE, DELETE or TRUNCATE. The repository exposes Append/Get only. DOWN removes the
trigger and function as well as tables, in dependency order. Database owners can administratively
disable triggers; production application credentials should not own tables or have DDL privileges.

Inbox stores the consumer/message identity and canonical payload hash, allowing the SQS consumer
to distinguish identical redeliveries from changed payloads. Completion is stored separately.
This phase does not implement message handling or duplicate-payload policy.

Outbox stores an immutable event identifier, JSON payload, timestamps and retry metadata. Phase 7
adds short SQL claims with leases and fencing tokens, described below. Exactly-once delivery is
not implied by this table.

## Financial use cases (phase 4)

`financial.Service` is registered by Fx using the existing `ports.UnitOfWork`. It has no imports
of PostgreSQL, pgx, HTTP or Fx. `CreateWallet`, `ProcessWager` and `ProcessHTTPWager` are application
entry points; no financial HTTP route is registered. The HTTP-named method adds durable request
idempotency to the same private financial processing core. Future transports can reuse that core
without duplicating wallet rules; this phase does not implement a message consumer.

### CreateWallet and internal OPENING

Input contains player, currency, initial Money and optional correlation ID. Validation uses the
existing Money/Wallet APIs; negative initial balances fail without persistence. The wallet is
constructed directly with its initial balance and version 1. No artificial Credit is applied.

For a positive balance, one SQL transaction inserts wallet, internal PROCESSED OPENING,
one CREDIT ledger (before 0, after initial balance, wallet version 1),
`WagerTransactionProcessed` and `WalletBalanceChanged`. For zero balance, it inserts only the
wallet. A duplicate player/currency returns `ErrWalletExists`. Failure anywhere rolls everything
back. The external processing API accepts BET, WIN, LOSS and (since phase 5) REFUND/ROLLBACK.
It rejects OPENING; providers cannot invoke the opening flow.

### BET, WIN and LOSS

Processing locks the wallet row, validates owner/currency, checks provider/external uniqueness,
creates a domain PENDING transaction, applies the domain operation and transitions to a terminal
state. Only the final transaction state is inserted, atomically with its other records.

| Operation | Valid amount | Successful effects |
| --- | --- | --- |
| BET | Positive | Debit, version +1, DEBIT ledger, PROCESSED, processed event and balance event |
| WIN | Positive | Credit, version +1, CREDIT ledger, PROCESSED, processed event and balance event |
| LOSS | Zero | PROCESSED and processed event; balance/version unchanged; no ledger or balance event |

BET without funds commits REJECTED / `INSUFFICIENT_FUNDS`. WIN balance overflow commits
REJECTED / `OVERFLOW`. Both preserve balance/version, omit ledger and balance event, and create
only `WagerTransactionRejected`. These committed rejections return a `WagerResult` with nil
execution error; `result.BusinessError()` maps their failure codes to stable application errors.
Returning a rejection as a UoW callback error would incorrectly roll back the rejection.
Invalid input, missing wallet, owner/currency mismatch and duplicate external identity return
stable application errors and cause no writes to commit. Infrastructure failures are translated
to application `ErrPersistence`; context cancellation/deadlines remain recognizable.

Phase 4 fixes one prior domain edge case: incrementing a wallet version at `math.MaxInt64`
previously wrapped to a negative number after changing balance. Credit/Debit now return
`wallet.ErrVersionOverflow` before assigning either field. The application persists the same
REJECTED / `OVERFLOW` policy. Regression tests exercise both operations and unchanged state.

### Persistent idempotency and canonical hash

Migration 3 creates `wager_idempotency_records`: global, case-sensitive `idempotency_key` primary
key, SHA-256 `payload_hash`, unique transaction FK, JSONB `result`, creation/completion timestamps.
Its check constraint keeps incomplete claim fields consistently NULL and completed fields all
present. Keys have no TTL or cleanup in this phase; callers must generate unique opaque keys.

Within the **same** financial transaction:

1. `INSERT ... ON CONFLICT(idempotency_key) DO NOTHING` claims the key.
2. A concurrent insertion waits on PostgreSQL uniqueness until the first transaction completes.
3. If the claim already exists, a separate READ COMMITTED SELECT obtains its committed record.
   Same hash returns the saved result with `idempotentReplay=true`; changed hash returns
   `ErrIdempotencyConflict`. Replay never locks or reads the current wallet balance.
4. A new claim validates and processes the operation through transaction-bound repositories.
5. It stores the original result, transaction ID and completion time before COMMIT.

Key conflicts take precedence over validation of a changed typed operation. New invalid requests
roll back their claim. Pending claims are never committed by these use cases. An administratively
inserted incomplete record fails closed with a persistence error. Claim/Complete require an active
transaction. A rollback releases the key so a later valid retry can execute.
The UoW explicitly begins READ COMMITTED transactions so that the SELECT after a conflicting
insert gets a fresh snapshot, even if the server/session default changes. A unit regression test
verifies this option. No in-memory map or mutex provides correctness.

Different keys using the same `(providerId, externalTransactionId)` return
`ErrDuplicateExternalTransaction`, even with identical payloads; they do not alias or replay the
original result. Wallet row locking handles the same-wallet race, and the existing unique
constraint handles races involving different wallets. Losing transactions roll back their claims
and all associated writes. The transport-independent method also enforces external uniqueness.

Hash version 1 serializes a fixed typed struct in this exact order: `version`, `providerId`,
`externalTransactionId`, `playerId`, `walletId`, `type`, `amountCents`, `currency`, `roundId`.
Money uses exact integer cents and its validated uppercase currency. Identifiers are preserved
exactly, with case-sensitive equality; there is no trimming/case folding of nonempty identities.
SHA-256 is returned as lowercase hexadecimal. Idempotency key, correlation ID, headers and transport
metadata are excluded. JSON property order, whitespace and decimal forms such as `20`, `20.0` and
`20.00` disappear when transport input becomes typed Money. Tests verify equivalence and verify
that changing every relevant business field changes the hash.

### Original observed balance

The completed JSONB result contains transaction/provider/external/wallet IDs, type, terminal state,
failure code, amount, observed balance, observed wallet version, processing time and replay flag.
Money is serialized as decimal string plus currency. Replays return the saved result and change
only the replay flag. A BET observing 80.00 continues to return 80.00 after a later WIN brings the
wallet to 130.00; this also applies to rejected operations and LOSS. Result/hash schema changes in
future deployments need compatibility for retained records.

### Outbox envelopes and atomicity across instances

Every event contains `eventId`, `eventType`, `aggregateId`, `correlationId`, `causationId`,
`occurredAt`, envelope `version=1` and typed `data`. Transaction events aggregate by transaction;
balance events aggregate by wallet. Causation is the transaction ID. Event IDs are stable strings
`<transactionId>:<eventType>`; ledger IDs are `<transactionId>:ledger`. Wallet/transaction IDs use
cryptographically random 128-bit hexadecimal values. Timestamps are UTC/RFC3339 with microsecond
precision. Monetary payloads contain decimal strings, never floating-point amounts.

`WalletBalanceChanged` data includes wallet ID, transaction ID, CREDIT/DEBIT direction, Money,
before/after balances and wallet version. Processed/rejected events include transaction identity,
type, terminal state, Money, and failure code when rejected. Correlation comes from the first
execution; replays create no events and do not replace it.

Idempotency claim/result, wallet, transaction, ledger and both outbox inserts use one `pgx.Tx`.
Nothing is published. PostgreSQL row locks and durable unique constraints coordinate independent
application instances. Different wallets and different keys do not share a global financial lock.
Integration tests use independent service instances and pools, verifying 100.00 against two 80.00
BETs, 50 concurrent identical requests, external duplicates with different keys, and progress on
wallet B while wallet A is demonstrably waiting for a row lock. Trigger-injected failures in
outbox insertion or idempotency completion verify total rollback, including an earlier event insert.

SQS, LocalStack, DLQ, publishers, OIDC, reconciliation, metrics/tracing and financial HTTP
endpoints remain outside the implemented phases.

## Reversals and pending references (phase 5)

REFUND and ROLLBACK extend the existing `ProcessWager`/`ProcessHTTPWager`; BET/WIN/LOSS and
CreateWallet retain their processing paths. Both first execution and pending retries call the
same `applyReversal` and `finishReversal` functions. No transport-specific financial logic exists.

### Reference validation and reversal effects

Reference lookup always uses `(providerId, referenceExternalTransactionId)` through the existing
repository. A valid reference must be PROCESSED, have the same player, wallet, currency and round,
and match the full amount exactly. Partial refunds/reversals are not supported.

| Request | Allowed reference | Movement |
| --- | --- | --- |
| REFUND | PROCESSED BET | CREDIT the entire BET amount |
| ROLLBACK | PROCESSED BET | CREDIT the entire BET amount |
| ROLLBACK | PROCESSED WIN | DEBIT the entire WIN amount |
| ROLLBACK | PROCESSED REFUND | DEBIT the entire REFUND amount |

LOSS, OPENING and another ROLLBACK cannot be reversed. Non-PROCESSED references are rejected
immediately when found, including PENDING_REFERENCE. Self-reference is rejected as invalid type.
External reversal amounts must be nonnegative; a valid reversible reference always has a positive
amount, and full equality is required. Zero amounts therefore cannot produce a movement; allowing
typed zero input permits a rollback of LOSS to persist `REFERENCE_TYPE_INVALID` rather than fail
before reference validation. Negative input is an application validation error.

Successful reversals increment wallet version exactly once and create one ledger entry plus
`WagerTransactionProcessed` and `WalletBalanceChanged`. Debit reversals without enough money
commit REJECTED / `INSUFFICIENT_FUNDS_FOR_REVERSAL`, preserving balance/version and creating only
the rejected event. Credit balance overflow or version overflow uses the existing `OVERFLOW` policy.
Rejected operations consume no successful reversal reservation, so a new external reversal can
succeed later if the circumstances change; replaying the rejected request retains its rejection.

### One successful direct reversal

Migration 4 creates `wager_reversals`, keyed by `original_transaction_id`, with a unique
`reversal_transaction_id`, FKs and creation time. Both REFUND and ROLLBACK share this reservation.
`INSERT ... ON CONFLICT(original_transaction_id) DO NOTHING` provides database uniqueness without
aborting the SQL transaction on a competing original ID. Application checks occur while holding
the original's wallet lock; the unique key also protects against competing reservation writes.
Reservation, wallet movement, ledger, terminal transaction, outbox and idempotent result all commit
or roll back together. A local tentative wallet change is discarded if reservation fails.

A BET may have **either** one REFUND **or** one ROLLBACK. Every later direct reversal is rejected
with `ALREADY_REVERSED`. A REFUND may itself have one ROLLBACK. Rolling back that REFUND does not
delete or reopen the BET's reservation. The original transaction remains PROCESSED and immutable;
reversal history is separate. This intentionally prevents repeated refund/rollback chains from
creating duplicate credit. No release/reversal-of-reservation API exists in this phase.

### Failure codes

| Failure code | Meaning |
| --- | --- |
| `REFERENCE_NOT_FOUND` | Hard TTL expired or maximum lookup attempts reached while reference remained absent |
| `REFERENCE_TYPE_INVALID` | Reference kind is prohibited, or self-reference |
| `REFERENCE_STATE_INVALID` | Found reference is not PROCESSED |
| `REFERENCE_PLAYER_MISMATCH` | Referenced player differs |
| `REFERENCE_WALLET_MISMATCH` | Referenced wallet differs |
| `REFERENCE_CURRENCY_MISMATCH` | Referenced currency differs |
| `REFERENCE_ROUND_MISMATCH` | Referenced round differs |
| `REFERENCE_AMOUNT_MISMATCH` | Full referenced amount differs |
| `ALREADY_REVERSED` | Successful direct reversal already exists |
| `INSUFFICIENT_FUNDS_FOR_REVERSAL` | Debit reversal would make balance negative |
| `OVERFLOW` | Credit balance or wallet version cannot be advanced safely |

`REFERENCE_PROVIDER_MISMATCH` is a defensive application validation code: provider-scoped lookup
cannot normally return another provider. A reference present only under another provider is absent
in the request's scope, becomes pending and may expire as REFERENCE_NOT_FOUND. Currency mismatch
on the same persisted wallet is also prevented by existing composite FKs; defensive checks remain.
OPENING has no provider/external ID, so provider lookup cannot address it; the type validator also
rejects it explicitly. Found invalid references produce terminal REJECTED results without movement.
`BusinessError()` classifies reference failures, already-reversed and reversal-funds failures using
stable application errors, without exposing PostgreSQL details.

### Persistent PENDING_REFERENCE and retry

An absent reference transitions PENDING -> PENDING_REFERENCE. No wallet balance/version change,
ledger or reservation is created. One `WagerTransactionPendingReference` event is inserted on this
initial transition, using the existing stable envelope and transaction/event-type ID. Missing-reference
retries create no further pending events. Terminal resolution creates processed/rejected events
and, for an actual movement, the balance event.

Migration 4 also creates `pending_wager_references`: transaction FK/PK, original correlation ID,
attempt count, snapshotted max attempts, first pending time, last attempt, next attempt, expiration
and completion timestamp. Completed metadata is retained for inspection. A partial index on
`(next_attempt_at, transaction_id) WHERE completed_at IS NULL` supports due-item selection.
The full operation is already persisted in `wager_transactions`, including its reference ID.
DOWN drops both auxiliary tables and their indexes/constraints, preserving migrations 1–3.

The initial failed lookup sets attempt count 0 and schedules `first_pending_at + base delay`.
Each worker lookup increments the count. After retry number N fails to find the reference, delay is
`min(base_delay * 2^(N-1), max_delay)`, calculated without duration overflow. Scheduling is clipped
to the expiration instant. Once the limit is reached without a reference, or the hard TTL is reached,
the operation becomes REJECTED / REFERENCE_NOT_FOUND with no movement. A retry beginning at/after
the hard deadline expires the request even if the reference has arrived by then. TTL and maximum
attempts are snapshotted for each pending; delay settings for subsequent retries come from the current
service policy. Technical failures roll the whole attempt back and are retried on a later poll.

| Environment variable | Default | Validation |
| --- | --- | --- |
| `PENDING_REFERENCE_POLL_INTERVAL` | `1s` | Positive duration |
| `PENDING_REFERENCE_BASE_DELAY` | `1s` | At least 1 microsecond |
| `PENDING_REFERENCE_MAX_DELAY` | `1m` | At least base delay |
| `PENDING_REFERENCE_MAX_ATTEMPTS` | `10` | Positive int32; retry lookups, excluding initial detection |
| `PENDING_REFERENCE_TTL` | `15m` | At least 1 microsecond |
| `PENDING_REFERENCE_BATCH_SIZE` | `50` | Positive int32 |

Sub-microsecond base delay/TTL are rejected because PostgreSQL timestamps have microsecond precision.
Defaults and environment overrides follow the existing config loader and are listed in `.env.example`.

### Multi-worker locking, recovery and lifecycle

`ResolvePendingOnce(ctx, now)` begins one existing UoW and claims one due metadata row using
`FOR UPDATE SKIP LOCKED`. Its lock remains held until commit/rollback. It then reads the transaction,
locks its wallet with `FOR UPDATE`, runs the shared reversal rules, writes financial records and
updates retry metadata and any idempotent result in that same transaction. It opens no nested UoW.
Workers claiming different pendings for one wallet serialize at that wallet row. Distinct wallets
remain independent; PostgreSQL uniqueness prevents duplicate successful reversals across instances.

The lock order is pending metadata -> wallet for retries. Fresh operations lock wallet and create
their own new pending row; they do not claim existing pending rows. Replays read saved results without
locking wallets. References are read without another wallet/transaction lock; valid references share
the already-locked wallet and terminal transactions are immutable. This avoids an inverse lock order
between normal operations and workers. If an original operation has not yet committed, wallet locking
determines whether the reversal finds the committed original or first persists pending for later retry.

`PendingWorker.RunOnce` processes up to the configured batch size and exposes deterministic execution
for tests; `ResolvePendingOnce` accepts an explicit UTC instant for scheduling tests. `Run` polls via
a ticker. Fx starts its one loop goroutine, cancels its context on stop and waits for completion before
closing the shared pool. No worker owns an additional pool, no nested goroutines are spawned by the
loop, and cancellation interrupts database waits. Essential scheduling state is exclusively in SQL;
a fresh service/worker can recover unfinished pendings after restart without the first process's memory.

### Pending and HTTP idempotency

A pending HTTP request saves its PENDING_REFERENCE result and observed balance. Same-key/same-hash
replay returns that saved snapshot while it remains pending. Changed payload conflicts, and a different
key for the same provider/external identity is still a duplicate-external conflict. Reference ID is
included in hash version 2 for REFUND/ROLLBACK. Phase 4 hashes remain byte-for-byte version 1 for
BET/WIN/LOSS without references, covered by a regression fixture; old idempotency records still replay.

When the worker reaches a terminal state, `UpdatePendingResult` replaces the saved pending JSON result
with its terminal result and balance/version observed at resolution, in the same pgx.Tx. Later replays
return terminal state, not a frozen pending response, and the terminal observed balance remains stable
despite later wallet changes. Replays concurrent with resolution see the committed snapshot before or
after it. Failed worker transactions leave both transaction and saved result pending. Non-HTTP pendings
have no idempotency record and resolve using the same logic.

Keycloak/OIDC, financial HTTP endpoints, reconciliation, advanced metrics and
tracing remain unimplemented.

## Phase 6: incoming SQS FIFO

The incoming transport uses AWS SDK for Go v2. Domain and financial application packages import
no AWS SDK. Both `ProcessHTTPWager` and `ProcessDelivery` call the same existing financial core
inside their own Unit of Work; there is no second implementation of wagering or reversal rules.

```mermaid
sequenceDiagram
    participant SQS as SQS FIFO
    participant C as Consumer
    participant A as financial.Service
    participant DB as PostgreSQL
    SQS->>C: ReceiveMessage (long poll)
    C->>C: Validate envelope, group and canonical hash
    C->>A: ProcessDelivery
    A->>DB: BEGIN; Inbox INSERT ON CONFLICT; SELECT FOR UPDATE
    A->>DB: Wallet lock; shared financial core; wager, ledger, Outbox
    A->>DB: Inbox completed; COMMIT
    DB-->>A: Durable result
    A-->>C: Accepted / duplicate / REJECTED / PENDING_REFERENCE
    C->>SQS: DeleteMessage
```

### Versioned message and identity

```json
{
  "schemaVersion": 1,
  "messageId": "delivery-bet-123",
  "providerId": "provider",
  "externalTransactionId": "bet-123",
  "playerId": "player",
  "walletId": "wallet",
  "type": "BET",
  "money": {"amount": "25.00", "currency": "BRL"},
  "roundId": "round",
  "correlationId": "correlation",
  "causationId": "producer-command-123",
  "occurredAt": "2026-01-01T00:00:00Z"
}
```

REFUND/ROLLBACK additionally require `referenceExternalTransactionId`. All other fields shown
are required. Types are BET/WIN/LOSS/REFUND/ROLLBACK; OPENING is internal only. Amount is a decimal
string parsed directly to integer cents; JSON numbers, extra fields, trailing JSON, unsupported
versions and malformed Money are permanent errors. LOSS requires zero, BET/WIN require positive
amounts, and reversal amounts follow the existing nonnegative Money/domain rules.

`Inbox.message_id` is the producer's **envelope messageId**, scoped by a stable consumer name.
The AWS-generated MessageId and receipt handle are transport identifiers only. Producers retain
the envelope ID and metadata on retries/republication, including after FIFO's deduplication window.
Changing consumer name changes the Inbox namespace; business external uniqueness still applies.

Hash version 1 is SHA-256 of JSON marshaled from the validated typed version-1 envelope in its
fixed field order. Money is normalized to two decimal places and timestamps to UTC. All envelope
fields, including correlation/causation/time, participate. Whitespace, JSON key order, equivalent
decimal notation and equivalent timestamp offsets do not affect the hash. AWS MessageId, receipt
and receive count are excluded. HTTP hashing remains separate and describes financial fields only.

### FIFO and concurrency

`MessageGroupId = "wallet:" + hex(SHA256(walletId))` preserves ordering within a wallet and permits
independent wallets to progress. `MessageDeduplicationId = "delivery:" + hex(SHA256(messageId))`
is stable, bounded to SQS ID limits and convenient for producer retries. `BuildSendInput` encodes
this incoming producer contract; it is not an Outbox publisher. The consumer checks that the
received group matches the wallet, then processes each received group in order. Separate groups
run in parallel up to `SQS_CONCURRENCY`. Failure stops the unacknowledged group's batch tail.

FIFO ordering applies to arrival within one group, not application timestamps, other groups or
HTTP requests. A missing-reference reversal can therefore still precede its original and become
PENDING_REFERENCE. PostgreSQL remains the authority for wallet serialization, external uniqueness
and successful reversals. A poison message blocks its group until redrive; after it moves to the DLQ,
later operations can proceed and the business sequence may have a gap. Operators must assess that
gap before replaying a DLQ message.

SQS provides **at-least-once delivery**. The application provides **exactly-once financial effects**
through PostgreSQL, Inbox, constraints and idempotency. This is not a claim of exactly-once delivery,
nor a guarantee of eventual success for permanently invalid requests.

### Atomic Inbox and recovery

The composite primary key `(consumer_name,message_id)` persists identity. `INSERT ... ON CONFLICT
DO NOTHING` waits on concurrent claims; a subsequent `SELECT ... FOR UPDATE` obtains the committed
row under READ COMMITTED. All participating repositories share the same `pgx.Tx`.

| Inbox state | Action |
| --- | --- |
| Absent | Claim, execute existing financial core, complete and commit |
| Completed, equal hash | Return duplicate, commit without new effects, then delete |
| Incomplete, equal hash | Lock and recover through the core; existing equivalent external operation completes Inbox without repeating effects |
| Any state, different hash | Permanent integrity error; rollback; preserve original hash; no delete |

Normal processing cannot commit an incomplete Inbox: its completion and all financial effects are
atomic. Recovery also handles an explicitly preexisting incomplete record, without process-local
ownership or a lease. Multiple instances contend on SQL rows, not a global mutex. Lock order is
Inbox then wallet; HTTP operations do not acquire Inbox locks and pending workers keep their existing
lock order. Business external uniqueness prevents duplicate effects even for distinct delivery IDs.

| Processing result | SQL and SQS policy |
| --- | --- |
| PROCESSED, persisted REJECTED, PENDING_REFERENCE | Complete Inbox, commit and delete; pending worker resolves later |
| Completed duplicate / equivalent external duplicate | No new financial effects; complete if needed, commit and delete |
| Transient SQL/connection/context/timeout error | Rollback; retain message for visibility expiry |
| Invalid JSON/version/Money/group, validation or integrity conflict | No financial effects; retain for bounded SQS redrive to DLQ |
| Delete failure after commit | Retain committed effects; redelivery reads completed Inbox and retries delete |

HTTP-first, equivalent SQS delivery returns `external_duplicate` with the original transaction ID
and state. SQS-first, a new HTTP Idempotency-Key returns the existing duplicate-external conflict.
An external identity with different financial fields is a permanent SQS payload conflict. HTTP
idempotency responses and Inbox records remain distinct; neither fabricates a historical balance
from the current wallet.

If the process stops before commit, PostgreSQL rolls back and a later delivery performs the operation.
If it stops after commit and before delete, a new instance sees completed Inbox and applies no effects.
No manual re-enqueue loop competes with SQS. AWS SDK retry is limited by request contexts, polling
transport failures have a cancellable delay, and message retries use visibility/redrive.

### LocalStack, visibility and shutdown

Compose preserves PostgreSQL and builds `localstack/localstack:4.6.0` with a versioned ready script.
It automatically creates `wager-transactions.fifo` and `wager-transactions-dlq.fifo`, FIFO with explicit
dedup IDs, redrive limit 5, DLQ retention 14 days and redrive allow policy restricted to the source queue.
Health checks verify both queues. LocalStack queue storage is ephemeral; PostgreSQL keeps its existing
volume. Configuration uses dummy local credentials; AWS deployments can use the SDK credential chain
or explicit credentials with an optional session token. No credentials are logged.

Defaults: long polling 20s, visibility 90s, batch 10, concurrency 4, processing deadline 5s per message
and ACK deadline 3s. HTTP client timeout is 25s to accommodate long polling. Config requires visibility
strictly greater than the worst sequential batch budget `batch * (processing + 3s)`, including time
spent waiting behind other messages/groups. Defaults reserve 80s plus a 10s margin. Processing timeouts
include SQL lock waits. There is no visibility-extension mechanism because work is bounded.

SQS is opt-in through `SQS_ENABLED` (false when absent, true in `.env.example`). Fx validates the main
queue is FIFO and targets the configured FIFO DLQ. On stop it cancels polling, starts no further batch
work and drains active messages using a separate context. If the stop deadline expires it cancels
active work and waits for cleanup before closing PostgreSQL. Uncommitted work never gets ACKed.

Structured JSON logs include consumer, durable/AWS message ID, correlation, wallet, provider,
transaction ID when available, receive count, outcome and classification. They contain neither full
payloads, monetary values nor raw SDK/SQL errors. Operational commands and failure-test methodology
are in [PHASE6_OPERATIONS.md](PHASE6_OPERATIONS.md).

### Real integration coverage

With both `TEST_DATABASE_URL` and `TEST_SQS_ENDPOINT`, tests use real PostgreSQL and LocalStack through
AWS SDK. They create isolated schemas and temporary FIFO/DLQ queues; no SQS or SQL mocks replace
these components. Tests cover all wagering paths, pending resolution, business rejections, incomplete
Inbox recovery, cross-transport conflict, hash conflicts, poison redrive, crash boundaries, invalid
receipt/delete failure, ordered FIFO batches, three independent pools/clients and shutdown draining.

The duplicate stress sends 50 real messages with **different FIFO dedup IDs** but identical durable
envelopes. It also runs 50 concurrent `Process` attempts using the actually received message across
three independent consumers/pools, then receives/deletes all 50 real messages. The concurrent calls
exercise SQL deduplication separately from FIFO's same-wallet delivery serialization. One debit,
one associated ledger entry, one transaction, one Inbox and one financial event set remain.

Crash simulation invokes `Process` through the real transport but deliberately omits ACK after its
commit, then recovers with a fresh consumer/pool. Precommit failures use SQL triggers at Outbox insert
and Inbox completion; delete failure sends an invalid receipt through the real SDK. Tests accelerate
redelivery with `ChangeMessageVisibility(...,0)` rather than long sleeps. This simulates the crash
boundary without forcibly terminating the Go test process. The parallelism test holds an actual wallet
row lock, observes its waiter in `pg_stat_activity`, lets two other consumers commit, cancels polling,
then releases the lock and verifies the active consumer safely commits and ACKs.

## Phase 7: Transactional Outbox publisher

Financial transactions continue to write their existing event envelopes alongside wallet, wager,
ledger and Inbox, committing them atomically. The publisher uses a **separate pool-backed store**
whose queries see only committed rows. No financial callback calls SQS, and no transport imports
are added to the financial application or domain. `ports.EventSender` separates publication
orchestration from AWS SDK; `ports.PublicationStore` separates it from pgx.

```mermaid
sequenceDiagram
    participant F as Financial use case
    participant DB as PostgreSQL
    participant P as Publisher
    participant Q as Domain events FIFO
    F->>DB: Wallet + wager + ledger + Inbox + Outbox
    F->>DB: COMMIT financial transaction
    P->>DB: Claim aggregate heads (SKIP LOCKED + durable lease)
    DB-->>P: Committed claims and persisted envelopes
    P->>DB: Renew current token before send; finish SQL
    P->>Q: SendMessage (no open SQL transaction)
    Q-->>P: Accepted
    P->>DB: Mark published_at conditionally on current live token
```

### Output destination and persisted envelope

LocalStack automatically provisions `wager-events.fifo`, FIFO with explicit deduplication and
14-day retention. `wager-transactions.fifo` remains input commands; its existing FIFO DLQ/redrive
remain unchanged. No downstream business consumer is added. Queue names and URLs must differ;
Fx also resolves ARNs and rejects an event destination equal to the actual command queue or its
DLQ, including when URL aliases bypass the name comparison. This prevents a command/event loop.

The publisher sends the JSONB payload **exactly as read**, without rebuilding its data or changing
eventId, timestamps, Money, version, causation or correlation. JSONB can normalize JSON formatting
when originally stored; publication does not change its persisted representation. The adapter
checks envelope IDs/type/aggregate/time against row metadata and requires version 1. All existing
WagerTransactionProcessed, WagerTransactionRejected, WalletBalanceChanged and
WagerTransactionPendingReference envelopes are supported. Money remains a decimal string;
financial timestamps are already UTC/RFC3339. The publisher builds no financial event of its own.

`MessageGroupId = "aggregate:" + hex(SHA256(aggregateId))`.
`MessageDeduplicationId = "event:" + hex(SHA256(eventId))`.
Both stay fixed on retries. Transaction state events use their existing transaction aggregateId;
WalletBalanceChanged uses walletId. There is no global group or cross-group order guarantee.

### Migration 000005, heads and leases

The existing retry fields cannot represent ownership after a transaction ends, so migration
`000005_outbox_publication_leases` adds `publication_sequence`, `claimed_at`, `claim_until`,
`claimed_by` and `claim_token`, with complete-or-empty lease constraints and an unpublished
aggregate/sequence index. Prior migrations and all event IDs/payloads are untouched. Existing rows
are backfilled deterministically by `(occurred_at,event_id)`; subsequent inserts get sequence
numbers automatically, with gaps allowed. DOWN removes publication metadata and the owned sequence
without deleting financial events. Deploy the migration before enabling publishers.

One autocommit CTE selects due, unpublished, unleased/expired rows using
`SELECT ... FOR UPDATE SKIP LOCKED` and updates their leases. Its anti-join excludes every event
having a smaller unpublished sequence for the **same aggregate**, even if that earlier event is
not due, leased, locked or retrying. Thus a claim batch contains at most one head per aggregate;
SKIP LOCKED cannot skip the head and select its tail. Each claimed head is sent concurrently, up
to the bounded batch size (default 10, maximum 100). Multiple publisher processes use independent
identities, pools and clients; all ownership needed for recovery is SQL state.

PostgreSQL statement time is authoritative for due/lease/mark decisions, avoiding dependence on
publisher-host clocks. Each Claim gets a fresh random token; the publisher instance gets its own
random identity at startup. Before sending, Renew requires a matching **unexpired** token. Mark
and Retry also require the current live token. A resumed stale owner cannot mark/retry/release a
new owner's recovered claim. Release is used only for claims whose sends have not started.
No SQL row lock, transaction or borrowed connection is held while SQS I/O is in progress.

### Ordering and its limits

For new financial events of one aggregate, publication follows insertion sequence. Financial
producers already serialize the wallet inside SQL, so later transactions cannot commit a smaller
same-wallet event while the earlier transaction remains uncommitted. Pending and terminal events
for one wager likewise follow its committed state transitions. Sequences are not a global commit
order across unrelated aggregates or arbitrary external SQL writers. Legacy events use the
documented timestamp/ID backfill; an old envelope did not record an original global insertion order.

An aggregate tail becomes eligible only after the head has a confirmed SendMessage **and** its
published_at was durably marked. A retrying head blocks its own tail while unrelated aggregates
proceed. SQS then preserves arrival order within the aggregate group. The resulting guarantee
concerns progression of distinct event IDs, not a strict order for every duplicate copy. A stalled
old sender, ambiguous timeout or very late broker request can cause an earlier event's duplicate
to arrive after later events. SQL fencing cannot cancel an already accepted/in-flight broker call.
Downstream consumers must deduplicate eventId and use aggregate state/version when appropriate;
there is no cross-aggregate or exactly-once broker guarantee.

### Retry and crash recovery

Send success is followed by conditional published_at persistence and lease clearing; retry_count
history is retained. A failed Send leaves published_at NULL, increments the stored failure counter,
sets next_attempt_at with capped exponential delay and clears the lease. The counter counts
**durably recorded failed send calls**, not claims, crashes, SDK internal retries or mark failures.
Its existing SQL INTEGER type saturates at 2,147,483,647 rather than overflowing. Delay doubling
also saturates safely. There is no maximum retry budget and no automatic discard: failure stays
auditable and retryable. A malformed legacy payload similarly remains pending and can block its
aggregate until repaired operationally. No second event is created to represent a retry.

Defaults are base 1s, cap 1m, polling 1s, lease 30s, send deadline 5s and SQL-operation deadline 3s.
Config requires the lease to exceed send + mark deadlines, leaving defaults a 22s margin after
renewal. Each aggregate head is sent in parallel, so it does not wait behind a sequential claimed
batch. Renew occurs immediately before Send; long continuous heartbeats are unnecessary for this
bounded work. SQL persistence failure can prevent recording a failed attempt; the lease then
expires and a fresh publisher recovers the unchanged event.

| Failure boundary | Durable state and recovery |
| --- | --- |
| Before financial commit / financial rollback | Publisher cannot see the event; no send |
| After claim, before send | Lease eventually expires; a new publisher claims and sends |
| Send fails | Retry count/date persist, lease clears; later due claim retries |
| Send accepted, process stops before mark | Event remains pending; expired lease causes another send with identical IDs |
| published_at update fails | Same recovery as an accepted send before mark; no false published state |
| Lease expired/reclaimed | Stale token cannot mutate the new owner's publication state |

Transactional Outbox provides **at-least-once event publication**. A successful publish followed
by a failed SQL mark can be repeated. FIFO's finite dedup window helps with immediate retries;
it does not replace durable downstream deduplication by eventId. There is no distributed SQL/SQS
transaction and no claim of exactly-once delivery. Eventual publication assumes the broker and
database recover and publishers continue running.

### Lifecycle, logs and verification

`OUTBOX_PUBLISHER_ENABLED` is false when absent and true in the local example. Fx validates the
event queue and starts the polling loop. Stop cancels polling, stops starting new sends, and lets
active sends/marks finish with a separate work context. Deadline expiry cancels active I/O, retaining
ambiguous sends for lease recovery. Unsent claims are released concurrently within one SQL timeout;
failed cleanup is recoverable by lease expiry. Fx waits for the loop and all item goroutines before
closing the pool. Existing SQS consumer and pending-reference worker remain composed in Fx.

Structured logs include eventId, eventType, aggregateId, correlationId, stored retryCount,
publisher identity, claim token and result (published, publish_failed, mark_failed, lease_lost).
They never print payloads, monetary values, credentials or raw SDK/SQL error details.

Real PostgreSQL + LocalStack tests cover uncommitted/rolled-back invisibility, all four financial
event types, due/head/lease eligibility, actual SKIP LOCKED behavior, persistent backoff, SQL fencing,
restart, crash before Send, accepted Send before mark, trigger-injected mark failure, no locks over
network I/O, independent aggregate progress, shutdown, and **100 financial events with three
independent publishers/pools/clients**. Every event is received with its persisted envelope/IDs,
all are marked and per-aggregate sequences stay ordered. SDK middleware only observes or pauses
real requests; successful calls always reach LocalStack. Crash tests explicitly observe two Send
calls with the same IDs; immediate duplicate delivery is suppressed by FIFO's dedup window, which
does not prove exactly-once publication. Tests expire SQL lease/retry timestamps directly to avoid
long sleeps, without replacing the database or broker with mocks.

Operations: [PHASE7_OPERATIONS.md](PHASE7_OPERATIONS.md).
Protocol references: [PostgreSQL SKIP LOCKED](https://www.postgresql.org/docs/16/sql-select.html),
[SQS FIFO identity/deduplication](https://docs.aws.amazon.com/AWSSimpleQueueService/latest/SQSDeveloperGuide/FIFO-key-terms.html).

## Testing notes

- Unit tests cover config parsing, pool config construction, and readiness behavior via a
  `DatabasePinger` test double. That double exercises HTTP readiness branches; it is not a fake
  PostgreSQL database.
- Domain unit tests cover Money, Wallet, and WagerTransaction invariants without infrastructure.
- Adapter unit tests cover mappings, structured error mapping, transaction-required guards,
  shared transaction binding, commit/rollback failures, panic and canceled-context cleanup.
- Real PostgreSQL integration tests cover UP/DOWN/UP, repository round trips, unique/check/FK
  constraints, append-only ledger, inbox, JSONB outbox, atomic rollback and concurrent row locking.
  Lock tests observe `pg_stat_activity` to confirm an actual lock wait instead of guessing via sleep.
  Tests create disposable schemas and never replace PostgreSQL with mocks. The test database user
  needs CREATE SCHEMA and access to its own sessions in `pg_stat_activity`.
- Financial unit tests cover canonical hashing, wallet/opening creation, BET/WIN/LOSS outcomes,
  validation, domain terminal states, event contents, saved-result replay, conflicts, rollback
  and application error boundaries. Unit doubles are sequential only; concurrency tests run
  against PostgreSQL with independent pools and services.

```powershell
docker compose --env-file .env.example up -d postgres
$env:TEST_DATABASE_URL = 'postgres://wagering:wagering@localhost:5432/wagering?sslmode=disable'
go test ./...
go vet ./...
go test -race ./...
```

Without `TEST_DATABASE_URL`, integration tests explicitly skip; the remaining suite still runs.
Do not treat a skipped integration suite as PostgreSQL validation.

## Environment limitations observed during phase 1

- Docker Compose PostgreSQL, migrations (`up` / `down` / re-`up`), and live readiness probes were
  validated successfully once Docker Desktop engine was running.
- `go test -race ./...` still requires CGO on Windows (`CGO_ENABLED=1` plus a C toolchain). This does
  not block unit tests or `go vet`.
