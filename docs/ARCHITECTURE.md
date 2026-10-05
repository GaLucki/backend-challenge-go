# Architecture — Phase 0 Foundation

## Objective

This service will process distributed wagering operations with strong financial guarantees.
Phase 0 delivers only a compilable foundation: configuration, Uber Fx composition, HTTP
health probes, structured logging, correlation IDs, and graceful shutdown.

Business rules (wallets, ledger, wager operations, SQS, OIDC, inbox/outbox) are intentionally
out of scope in this phase.

## Package structure

| Path | Responsibility |
| --- | --- |
| `cmd/api` | Process entrypoint; constructs and runs the Fx application |
| `internal/config` | Environment-based configuration loading and validation |
| `internal/application` | Fx module composition for the process |
| `internal/adapter/http` | HTTP server, health endpoints, error envelope, middleware |
| `internal/observability` | JSON logger and context helpers for request identifiers |
| `docs/` | Technical decisions and architecture notes |

`internal/domain` and `internal/ports` are reserved for later phases and were not created empty.

## Role of Uber Fx

Uber Fx owns dependency injection and process lifecycle:

- `fx.Provide` registers constructors (config, logger, HTTP handler, readiness flag)
- `fx.Invoke` registers the HTTP server lifecycle hooks
- `fx.Lifecycle` starts the listener on `OnStart` and shuts it down on `OnStop`
- SIGINT/SIGTERM are handled by `fx.App.Run()`, which triggers ordered shutdown

Domain logic must remain independent of Fx. The `application` package is the composition root.

## Startup flow

1. `main` creates `fx.New(application.Module)` and calls `Run()`
2. Config is loaded from environment variables and validated
3. JSON logger is created from `LOG_LEVEL`
4. HTTP routes and middleware are assembled
5. Lifecycle `OnStart` binds the TCP listener, serves HTTP, and marks readiness as true

## Lifecycle and shutdown

On SIGINT/SIGTERM:

1. Fx begins stop hooks in reverse dependency order
2. HTTP readiness is marked false
3. `http.Server.Shutdown` drains in-flight requests within `SHUTDOWN_TIMEOUT` (default `10s`)
4. Remaining dependencies are released after dependents stop

Later phases will register additional lifecycle hooks for SQS consumers, workers, publishers,
and database pools using the same Fx lifecycle mechanism.

## Health endpoints

| Endpoint | Meaning in phase 0 |
| --- | --- |
| `GET /health/live` | Process is alive |
| `GET /health/ready` | Application finished initialization and HTTP server started |

Readiness does not check PostgreSQL or SQS yet.

## Decisions in this phase

- Go module path: `github.com/junglegaming/backend-challenge-go`
- Go version: `1.27` (environment-installed stable toolchain)
- HTTP stack: standard library `net/http` with Go ServeMux method patterns
- Logging: `log/slog` JSON handler (no extra logging dependency)
- Correlation ID: `X-Correlation-ID` middleware with context propagation
- Configuration: environment variables with defaults for local development
- Minimal dependencies: only Uber Fx (and its transitive modules) beyond the standard library
- No PostgreSQL, SQS, Keycloak, wallet, or wager implementations yet

## Configuration

| Variable | Required | Default | Notes |
| --- | --- | --- | --- |
| `APP_ENV` | yes* | `development` | Empty value is rejected |
| `HTTP_PORT` | yes* | `8080` | Must be `1..65535` |
| `LOG_LEVEL` | yes* | `info` | `debug`, `info`, `warn`, `error` |
| `SHUTDOWN_TIMEOUT` | no | `10s` | Go duration string |
| `DATABASE_URL` | no | — | Loaded, unused in phase 0 |
| `KEYCLOAK_URL` | no | — | Loaded, unused in phase 0 |
| `SQS_ENDPOINT` | no | — | Loaded, unused in phase 0 |
| `AWS_REGION` | no | — | Loaded, unused in phase 0 |
| `AWS_ACCESS_KEY_ID` | no | — | Loaded, unused in phase 0 |
| `AWS_SECRET_ACCESS_KEY` | no | — | Loaded, unused in phase 0 |

\* Required after defaults are applied; explicitly empty values fail validation.

## Local run

```sh
go run ./cmd/api
```

```sh
go test ./...
go vet ./...
```
