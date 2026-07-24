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
	"github.com/munisp/hydrogenTransport/services/go/fleet-api/internal/auth"
	"github.com/munisp/hydrogenTransport/services/go/fleet-api/internal/config"
	"github.com/munisp/hydrogenTransport/services/go/fleet-api/internal/gate"
	"github.com/munisp/hydrogenTransport/services/go/fleet-api/internal/handlers"
)

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

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
	jwtmw := auth.New(cfg.KeycloakIssuer, log)
	h := handlers.New(pool, log)
	px := handlers.NewProxies(
		envOr("TWIN_URL", "http://digital-twin:8092"),
		envOr("OPTIMIZER_URL", "http://route-optimizer:8091"),
		log,
	)

	r := chi.NewRouter()
	r.Use(middleware.RequestID, middleware.RealIP, middleware.Recoverer, middleware.Timeout(30*time.Second))
	r.Get("/healthz", h.Healthz)

	// telematics module: vehicles + telemetry queries
	r.Group(func(r chi.Router) {
		r.Use(gate.Module(tc, "telematics"))
		r.Get("/v1/vehicles", h.ListVehicles)
		r.Get("/v1/vehicles/{id}", h.GetVehicle)
		r.Get("/v1/vehicles/{id}/telemetry", h.GetTelemetry)
		r.Get("/v1/telemetry/latest", h.LatestTelemetry)
	})
	// digital-twin module: proxy to the Rust digital-twin service
	r.With(gate.Module(tc, "digital-twin")).Get("/v1/vehicles/{id}/twin", px.GetTwin)
	// predictive-maintenance module
	r.With(gate.Module(tc, "predictive-maintenance")).Get("/v1/maintenance/predictions", h.ListPredictions)
	// fuel-monitoring module
	r.With(gate.Module(tc, "fuel-monitoring")).Get("/v1/fuel/levels", h.ListFuelLevels)
	// route-energy-optimizer module (mutating → Keycloak JWT)
	r.With(gate.Module(tc, "route-energy-optimizer"), jwtmw.RequireAuth).
		Post("/v1/optimize/route", px.OptimizeRoute)

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
