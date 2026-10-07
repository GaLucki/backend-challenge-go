package httpadapter

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/junglegaming/backend-challenge-go/internal/config"
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
	server := &http.Server{
		Addr:              p.Config.Addr(),
		Handler:           p.Handler,
		ReadHeaderTimeout: 5 * time.Second,
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
				if err := server.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
					p.Logger.Error("http server stopped unexpectedly", "error", err)
				}
			}()

			p.Ready.Store(true)
			return nil
		},
		OnStop: func(ctx context.Context) error {
			p.Ready.Store(false)
			p.Logger.InfoContext(ctx, "http server shutting down")

			shutdownCtx, cancel := context.WithTimeout(ctx, p.Config.ShutdownTimeout)
			defer cancel()

			return server.Shutdown(shutdownCtx)
		},
	})
}

// NewHandler builds the root HTTP handler with middleware and routes.
func NewHandler(health *HealthHandler, logger *slog.Logger, auth *AuthMiddleware) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health/live", health.Live)
	mux.HandleFunc("GET /health/ready", health.Ready)
	mux.Handle("GET /auth/me", auth.Authenticate(http.HandlerFunc(ProbeHandler)))
	mux.Handle("GET /auth/internal", auth.Authenticate(auth.RequireInternal(http.HandlerFunc(ProbeHandler))))
	mux.Handle("GET /auth/providers/{providerId}", auth.Authenticate(auth.AuthorizeProvider(func(r *http.Request) string { return r.PathValue("providerId") }, http.HandlerFunc(ProbeHandler))))

	root := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, pattern := mux.Handler(r); pattern == "" {
			WriteError(w, http.StatusNotFound, ErrorCodeNotFound, "resource not found")
			return
		}
		mux.ServeHTTP(w, r)
	})

	return CorrelationMiddleware(requestLoggingMiddleware(logger, root))
}

func requestLoggingMiddleware(logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)

		reqLogger := observabilityLogger(r.Context(), logger)
		reqLogger.Info("http request completed",
			"method", r.Method,
			"path", r.URL.Path,
			"durationMs", time.Since(start).Milliseconds(),
		)
	})
}
