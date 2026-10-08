package application

import (
	"log/slog"
	"sync/atomic"

	"context"
	"github.com/jackc/pgx/v5/pgxpool"
	httpadapter "github.com/junglegaming/backend-challenge-go/internal/adapter/http"
	oidcadapter "github.com/junglegaming/backend-challenge-go/internal/adapter/oidc"
	"github.com/junglegaming/backend-challenge-go/internal/adapter/postgres"
	sqsadapter "github.com/junglegaming/backend-challenge-go/internal/adapter/sqs"
	"github.com/junglegaming/backend-challenge-go/internal/application/financial"
	"github.com/junglegaming/backend-challenge-go/internal/config"
	"github.com/junglegaming/backend-challenge-go/internal/identity"
	"github.com/junglegaming/backend-challenge-go/internal/observability"
	"github.com/junglegaming/backend-challenge-go/internal/ports"
	"go.uber.org/fx"
)

// Module composes the application dependency graph.
var Module = fx.Options(
	fx.Provide(
		config.Load,
		newLogger,
		newReadyFlag,
		observability.NewMetrics,
		postgres.NewPool,
		postgres.NewRepositories,
		postgres.NewUnitOfWork,
		postgres.NewPublicationStore,
		postgres.NewOperationalReader,
		NewOperationalCollector,
		newFinancialService,
		newPendingWorker,
		identity.NewAuthorizer,
		newOIDCVerifier,
		financial.NewAuthorizedService,
		postgres.NewFinancialReader,
		financial.NewReadService,
		postgres.NewReconciliationReader,
		newReconciliationService,
		httpadapter.NewFinancialHandler,
		httpadapter.NewAuthMiddleware,
		func(r ports.Repositories) ports.ReversalRepository { return r.Reversals },
		func(r ports.Repositories) ports.PendingReferenceRepository { return r.PendingReferences },
		func(r ports.Repositories) ports.IdempotencyRepository { return r.Idempotency },
		func(r ports.Repositories) ports.WalletRepository { return r.Wallets },
		func(r ports.Repositories) ports.WagerTransactionRepository { return r.Wagers },
		func(r ports.Repositories) ports.LedgerRepository { return r.Ledger },
		func(r ports.Repositories) ports.InboxRepository { return r.Inbox },
		func(r ports.Repositories) ports.OutboxRepository { return r.Outbox },
		func(pool *pgxpool.Pool) httpadapter.DatabasePinger { return pool },
		newHealthHandler,
		httpadapter.NewMetricsHandler,
		httpadapter.NewHandler,
	),
	fx.Invoke(httpadapter.RegisterServer, RegisterPendingWorker, RegisterSQSConsumer, RegisterOutboxPublisher, RegisterOperationalCollector, httpadapter.RegisterReadiness),
)

func newOIDCVerifier(lc fx.Lifecycle, cfg config.Config) (identity.Authenticator, error) {
	v, err := oidcadapter.NewVerifier(cfg)
	if err != nil {
		return nil, err
	}
	lc.Append(fx.Hook{OnStart: v.Initialize})
	return v, nil
}

func newLogger(cfg config.Config) *slog.Logger {
	return observability.NewLogger(cfg.LogLevel)
}

func newReadyFlag() *atomic.Bool {
	return &atomic.Bool{}
}

func newHealthHandler(ready *atomic.Bool, db httpadapter.DatabasePinger, cfg config.Config, metrics *observability.Metrics) (*httpadapter.HealthHandler, error) {
	h := httpadapter.NewHealthHandler(ready, db, cfg).WithTelemetry(metrics)
	if cfg.SQSEnabled || cfg.OutboxEnabled {
		broker, err := sqsadapter.NewHealthPinger(context.Background(), cfg)
		if err != nil {
			return nil, err
		}
		h.WithBroker(broker)
	}
	return h, nil
}
func newReconciliationService(reader ports.ReconciliationReader, auth *identity.Authorizer, metrics *observability.Metrics) *financial.ReconciliationService {
	return financial.NewReconciliationService(reader, auth).WithTelemetry(metrics)
}
