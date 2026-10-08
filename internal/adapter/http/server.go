package httpadapter

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/junglegaming/backend-challenge-go/internal/config"
	"github.com/junglegaming/backend-challenge-go/internal/observability"
	"go.uber.org/fx"
)

// ServerParams contains dependencies for the HTTP server.
type ServerParams struct {
	fx.In

	Lifecycle fx.Lifecycle
	Config    config.Config
	Logger    *slog.Logger
	Handler   http.Handler
	Ready     *atomic.Bool
}

// RegisterServer wires the HTTP server into the Fx lifecycle.
func RegisterServer(p ServerParams) {
	drain := &drainingHandler{next: p.Handler}
	serveDone := make(chan struct{})
	server := &http.Server{
		Addr:              p.Config.Addr(),
		Handler:           drain,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    32 << 10,
	}

	p.Lifecycle.Append(fx.Hook{
		OnStart: func(ctx context.Context) error {
			ln, err := net.Listen("tcp", server.Addr)
			if err != nil {
				return err
			}

			p.Logger.InfoContext(ctx, "http server starting",
				"addr", server.Addr,
				"appEnv", p.Config.AppEnv,
			)

			go func() {
				defer close(serveDone)
				if err := server.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
					p.Logger.Error("http server stopped unexpectedly", "failureClass", "listener_failure")
				}
			}()

			return nil
		},
		OnStop: func(ctx context.Context) error {
			p.Ready.Store(false)
			p.Logger.InfoContext(ctx, "http server shutting down")

			shutdownCtx, cancel := context.WithTimeout(ctx, p.Config.ShutdownTimeout)
			defer cancel()

			drain.stopAccepting()
			err := server.Shutdown(shutdownCtx)
			if err != nil {
				_ = server.Close()
			}
			// Join handlers, including canceled SQL rollback cleanup, before Fx
			// can close the shared pool. Each request has a bounded context.
			drain.active.Wait()
			<-serveDone
			return err
		},
	})
}

type drainingHandler struct {
	next     http.Handler
	mu       sync.Mutex
	stopping bool
	active   sync.WaitGroup
}

func (h *drainingHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.mu.Lock()
	if h.stopping {
		h.mu.Unlock()
		w.Header().Set("X-Content-Type-Options", "nosniff")
		WriteError(w, 503, "SERVICE_UNAVAILABLE", "application shutting down")
		return
	}
	h.active.Add(1)
	h.mu.Unlock()
	defer h.active.Done()
	h.next.ServeHTTP(w, r)
}
func (h *drainingHandler) stopAccepting() { h.mu.Lock(); h.stopping = true; h.mu.Unlock() }

// NewHandler builds the root HTTP handler with middleware and routes.
func NewHandler(health *HealthHandler, logger *slog.Logger, auth *AuthMiddleware, financial *FinancialHandler, metrics *MetricsHandler) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health/live", health.Live)
	mux.HandleFunc("GET /health/ready", health.Ready)
	if metrics != nil && metrics.metrics.Enabled {
		mux.Handle("GET /metrics", auth.Authenticate(auth.RequireInternal(metrics)))
	}
	mux.Handle("GET /auth/me", auth.Authenticate(http.HandlerFunc(ProbeHandler)))
	mux.Handle("GET /auth/internal", auth.Authenticate(auth.RequireInternal(http.HandlerFunc(ProbeHandler))))
	mux.Handle("GET /auth/providers/{providerId}", auth.Authenticate(auth.AuthorizeProvider(func(r *http.Request) string { return r.PathValue("providerId") }, http.HandlerFunc(ProbeHandler))))
	mux.Handle("POST /wallets", auth.Authenticate(http.HandlerFunc(financial.CreateWallet)))
	mux.Handle("GET /wallets/{walletId}", auth.Authenticate(http.HandlerFunc(financial.GetWallet)))
	mux.Handle("GET /wallets/{walletId}/ledger", auth.Authenticate(http.HandlerFunc(financial.Ledger)))
	mux.Handle("POST /wallets/{walletId}/reconciliation", auth.Authenticate(http.HandlerFunc(financial.Reconciliation)))
	mux.Handle("POST /wagering/transactions", auth.Authenticate(http.HandlerFunc(financial.ProcessWager)))
	mux.Handle("GET /wagering/transactions/{transactionId}", auth.Authenticate(http.HandlerFunc(financial.GetTransaction)))
	mux.Handle("GET /providers/{providerId}/wagering/transactions/{externalTransactionId}", auth.Authenticate(http.HandlerFunc(financial.GetExternalTransaction)))

	root := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if _, pattern := mux.Handler(r); pattern == "" {
			var allowed []string
			for _, method := range []string{"GET", "HEAD", "POST"} {
				copy := r.Clone(r.Context())
				copy.Method = method
				if _, pattern := mux.Handler(copy); pattern != "" {
					allowed = append(allowed, method)
				}
			}
			if len(allowed) > 0 {
				w.Header().Set("Allow", strings.Join(allowed, ", "))
				WriteError(w, 405, "METHOD_NOT_ALLOWED", "method not allowed")
				return
			}
			WriteError(w, http.StatusNotFound, ErrorCodeNotFound, "resource not found")
			return
		}
		mux.ServeHTTP(w, r)
	})

	return CorrelationMiddleware(requestLoggingMiddleware(logger, instrumentHTTP(metrics, root)))
}

func instrumentHTTP(handler *MetricsHandler, next http.Handler) http.Handler {
	if handler == nil || !handler.metrics.Enabled {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		m := handler.metrics
		m.InFlight("http_in_flight", 1)
		defer m.InFlight("http_in_flight", -1)
		recorder := &statusWriter{ResponseWriter: w}
		next.ServeHTTP(recorder, r)
		route := r.Pattern
		if _, path, ok := strings.Cut(route, " "); ok {
			route = path
		}
		status := recorder.status
		if status == 0 {
			status = 200
		}
		m.Count("http_requests_total", r.Method, route, strconv.Itoa(status))
		m.Duration("http_request_duration_seconds", time.Since(started), r.Method, route)
	})
}

// App initialization becomes ready only after all required Fx OnStart hooks.
// This final hook marks unready before workers drain during reverse-order stop.
func RegisterReadiness(lc fx.Lifecycle, ready *atomic.Bool, metrics *observability.Metrics) {
	lc.Append(fx.Hook{OnStart: func(context.Context) error { ready.Store(true); return nil }, OnStop: func(context.Context) error { ready.Store(false); metrics.Set("ready", 0); return nil }})
}

func requestLoggingMiddleware(logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		info := &requestInfo{}
		ctx, cancel := context.WithTimeout(context.WithValue(r.Context(), requestInfoKey{}, info), 10*time.Second)
		defer cancel()
		r = r.WithContext(ctx)
		recorder := &statusWriter{ResponseWriter: w}
		next.ServeHTTP(recorder, r)

		reqLogger := observabilityLogger(r.Context(), logger)
		status := recorder.status
		if status == 0 {
			status = 200
		}
		route := r.Pattern
		if route == "" {
			route = "unmatched"
		}
		attrs := []any{
			"method", r.Method,
			"route", route,
			"status", status,
			"durationMs", time.Since(start).Milliseconds(),
			"providerId", info.ProviderID, "transactionId", info.TransactionID, "walletId", info.WalletID, "idempotentReplay", info.Replay,
		}
		if info.ReconciliationStatus != "" {
			attrs = append(attrs, "reconciliationStatus", info.ReconciliationStatus, "ledgerEntriesChecked", info.LedgerEntriesChecked, "transactionsChecked", info.TransactionsChecked, "divergenceCount", info.DivergenceCount)
		}
		reqLogger.Info("http request completed", attrs...)
	})
}

type requestInfoKey struct{}
type requestInfo struct {
	ProviderID, TransactionID, WalletID                        string
	Replay                                                     bool
	ReconciliationStatus                                       string
	LedgerEntriesChecked, TransactionsChecked, DivergenceCount int
}

func requestInfoFromContext(ctx context.Context) *requestInfo {
	info, _ := ctx.Value(requestInfoKey{}).(*requestInfo)
	return info
}
func setRequestResult(r *http.Request, tx, wallet string, replay bool, provider ...string) {
	if info := requestInfoFromContext(r.Context()); info != nil {
		info.TransactionID = tx
		info.WalletID = wallet
		info.Replay = replay
		if info.ProviderID == "" && len(provider) > 0 {
			info.ProviderID = provider[0]
		}
	}
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(status int) {
	if w.status != 0 {
		return
	}
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}
func (w *statusWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(200)
	}
	return w.ResponseWriter.Write(b)
}
func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
