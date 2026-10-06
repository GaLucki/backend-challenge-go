# Architecture

## Objective

This service will process distributed wagering operations with strong financial guarantees.
The current foundation covers process composition, HTTP health probes, structured logging,
correlation IDs, graceful shutdown, and PostgreSQL persistence infrastructure.

The financial domain (`Money`, `Wallet`, `WagerTransaction`) is implemented in-process and remains
independent of persistence and transport. Ledger, inbox/outbox, SQS, OIDC, and wagering HTTP APIs
remain out of scope until later phases.

## Package structure

| Path | Responsibility |
| --- | --- |
| `cmd/api` | Process entrypoint; constructs and runs the Fx application |
| `internal/config` | Environment-based configuration loading and validation |
| `internal/application` | Fx module composition for the process |
| `internal/adapter/http` | HTTP server, health endpoints, error envelope, middleware |
| `internal/adapter/postgres` | pgxpool connection lifecycle and pool configuration |
| `internal/domain/money` | Immutable monetary value object (int64 cents + currency) |
| `internal/domain/wallet` | Wallet aggregate (credit/debit, version, rehydration) |
| `internal/domain/wager` | Wager transaction entity and explicit state machine |
| `internal/observability` | JSON logger and context helpers for request identifiers |
| `migrations/` | Versioned SQL migrations (`up` / `down`) |
| `docs/` | Technical decisions and architecture notes |

`internal/ports` remains reserved for later phases.

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

The application does **not** run migrations on startup. Schema changes are applied by the
deploy/infrastructure process before the service starts.

## Lifecycle and shutdown

On SIGINT/SIGTERM:

1. Fx begins stop hooks in reverse dependency order
2. HTTP readiness is marked false and in-flight requests drain within `SHUTDOWN_TIMEOUT`
3. PostgreSQL pool is closed
4. Remaining dependencies are released after dependents stop

Later phases will register additional lifecycle hooks for SQS consumers, workers, and publishers
using the same Fx lifecycle mechanism.

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

Phase 1 includes only a technical bootstrap table (`app_metadata`) to validate the migration
mechanism. Financial tables are intentionally deferred until the domain model is defined.

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

Persistence of wallets/transactions/ledger is intentionally not implemented in this phase.

## Testing notes

- Unit tests cover config parsing, pool config construction, and readiness behavior via a
  `DatabasePinger` test double. That double exercises HTTP readiness branches; it is not a fake
  PostgreSQL database.
- Domain unit tests cover Money, Wallet, and WagerTransaction invariants without infrastructure.
- Real PostgreSQL validation requires Docker Compose (or an equivalent Postgres instance) and is
  documented above. Integration against a live database is performed manually when containers are
  available.

## Environment limitations observed during phase 1

- Docker Compose PostgreSQL, migrations (`up` / `down` / re-`up`), and live readiness probes were
  validated successfully once Docker Desktop engine was running.
- `go test -race ./...` still requires CGO on Windows (`CGO_ENABLED=1` plus a C toolchain). This does
  not block unit tests or `go vet`.
