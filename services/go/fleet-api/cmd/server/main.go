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

	toggle "github.com/munisp/hydrogenTransport/packages/toggle-client/go"
	"github.com/munisp/hydrogenTransport/services/go/fleet-api/internal/config"
	"github.com/munisp/hydrogenTransport/services/go/fleet-api/internal/gate"
	"github.com/munisp/hydrogenTransport/services/go/fleet-api/internal/handlers"
	"github.com/munisp/hydrogenTransport/services/go/fleet-api/internal/metrics"
)

func main() {
	log, _ := zap.NewProduction()
	defer func() { _ = log.Sync() }()

	cfg := config.Load("8081")
	if cfg.DatabaseURL == "" {
		log.Fatal("DATABASE_URL is required")
	}
	if cfg.ToggleURL == "" {
		log.Warn("TOGGLE_URL not set; toggle client is fail-closed so all module routes will 404")
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

	tc := toggle.New(cfg.ToggleURL)
	h := handlers.New(pool, log)

	r := chi.NewRouter()
	r.Use(middleware.RequestID, middleware.RealIP, middleware.Recoverer, metrics.Middleware("fleet-api"), middleware.Timeout(30*time.Second))
	r.Get("/healthz", h.Healthz)
	r.Handle("/metrics", metrics.Handler())

	// telematics module: vehicles + telemetry queries. NB: per-bus
	// GET /v1/vehicles/{id} and /v1/vehicles/{id}/telemetry were removed
	// (BUSINESS_LOGIC_AUDIT orphan inventory): zero PWA/mobile callers —
	// the live map consumes /v1/telemetry/latest only.
	r.Group(func(r chi.Router) {
		r.Use(gate.Module(tc, "telematics"))
		r.Get("/v1/vehicles", h.ListVehicles)
		r.Get("/v1/telemetry/latest", h.LatestTelemetry)
	})
	// predictive-maintenance module
	r.With(gate.Module(tc, "predictive-maintenance")).Get("/v1/maintenance/predictions", h.ListPredictions)
	// fuel-monitoring module
	r.With(gate.Module(tc, "fuel-monitoring")).Get("/v1/fuel/levels", h.ListFuelLevels)
	// NB: the digital-twin and route-optimizer proxy routes were removed
	// (audit orphan inventory): APISIX routes /api/twin/* and
	// /api/optimize/* directly to those services and the PWA calls them
	// there; the fleet-api proxies had zero callers.

	srv := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           r,
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		log.Info("fleet-api listening", zap.String("addr", srv.Addr))
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
	log.Info("fleet-api stopped")
}
