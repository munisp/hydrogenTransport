// audit-log — append-only, hash-chained audit trail for H2Fleet. Port 8086
// (gateway prefix /api/audit/*). See README.md for the full contract.
package main

import (
	"context"
	"errors"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/zap"

	auth "github.com/munisp/hydrogenTransport/packages/go-auth"
	"github.com/munisp/hydrogenTransport/services/go/audit-log/internal/anomaly"
	"github.com/munisp/hydrogenTransport/services/go/audit-log/internal/config"
	"github.com/munisp/hydrogenTransport/services/go/audit-log/internal/handlers"
	"github.com/munisp/hydrogenTransport/services/go/audit-log/internal/metrics"
	"github.com/munisp/hydrogenTransport/services/go/audit-log/internal/mirror"
	"github.com/munisp/hydrogenTransport/services/go/audit-log/internal/store"
)

func main() {
	log, _ := zap.NewProduction()
	defer func() { _ = log.Sync() }()

	cfg := config.Load()
	if cfg.DatabaseURL == "" {
		log.Fatal("DATABASE_URL is required")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pool, err := pgxpool.New(ctx, cfg.DatabaseURL)
	if err != nil {
		log.Fatal("connect postgres", zap.Error(err))
	}
	defer pool.Close()
	if err := pool.Ping(ctx); err != nil {
		log.Fatal("ping postgres", zap.Error(err))
	}

	st := store.NewPGStore(pool)
	if err := st.EnsureSchema(ctx); err != nil {
		log.Fatal("ensure platform.audit_log schema", zap.Error(err))
	}

	mir := mirror.New(cfg.OpenSearchURL, cfg.OpenSearchIndex, log)
	det := anomaly.New(cfg.AnomalyThreshold, cfg.AnomalyWindow, cfg.AlertmanagerURL, log)
	jwtmw := auth.New(cfg.KeycloakIssuer, log)
	h := handlers.New(st, mir, det, log)

	r := chi.NewRouter()
	r.Use(middleware.RequestID, middleware.RealIP, middleware.Recoverer,
		metrics.Middleware("audit-log"), middleware.Timeout(15*time.Second))
	r.Get("/healthz", h.Healthz)
	r.Handle("/metrics", metrics.Handler())

	// Ingest: shared service token (X-Audit-Token) OR any valid JWT.
	r.With(handlers.RequireIngestAuth(cfg.IngestToken, jwtmw.RequireAuth)).
		Post("/v1/audit", h.Ingest)

	// Reads + integrity verification: platform-admin only.
	r.Group(func(r chi.Router) {
		r.Use(jwtmw.RequireRole("platform-admin"))
		r.Get("/v1/audit", h.List)
		r.Get("/v1/audit/verify", h.Verify)
	})

	srv := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           r,
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		log.Info("audit-log listening", zap.String("addr", srv.Addr))
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatal("http server", zap.Error(err))
		}
	}()

	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Error("graceful shutdown", zap.Error(err))
	}
	log.Info("audit-log stopped")
}
