package main

import (
	"context"
	"crypto/subtle"
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
	"github.com/munisp/hydrogenTransport/services/go/infra-api/internal/auth"
	"github.com/munisp/hydrogenTransport/services/go/infra-api/internal/config"
	"github.com/munisp/hydrogenTransport/services/go/infra-api/internal/events"
	"github.com/munisp/hydrogenTransport/services/go/infra-api/internal/gate"
	"github.com/munisp/hydrogenTransport/services/go/infra-api/internal/handlers"
	"github.com/munisp/hydrogenTransport/services/go/infra-api/internal/workflow"
)

func main() {
	log, _ := zap.NewProduction()
	defer func() { _ = log.Sync() }()

	cfg := config.Load("8082")
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
	pub := events.NewPublisher(cfg.KafkaBrokers, "infra-api", log)
	defer pub.Close()
	wf := workflow.NewSignaler(os.Getenv("TEMPORAL_HOST"), log)
	defer wf.Close()

	h := handlers.New(pool, pub, wf, log)
	if err := h.EnsureSchema(ctx); err != nil {
		log.Fatal("ensure schema", zap.Error(err))
	}

	// The leak webhook accepts either a shared sensor token
	// (LEAK_INGEST_TOKEN via X-Sensor-Token header) or a Keycloak JWT.
	leakToken := os.Getenv("LEAK_INGEST_TOKEN")
	leakAuth := func(next http.Handler) http.Handler {
		if leakToken == "" {
			return jwtmw.RequireAuth(next)
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if subtle.ConstantTimeCompare([]byte(r.Header.Get("X-Sensor-Token")), []byte(leakToken)) == 1 {
				next.ServeHTTP(w, r)
				return
			}
			jwtmw.RequireAuth(next).ServeHTTP(w, r)
		})
	}

	r := chi.NewRouter()
	r.Use(middleware.RequestID, middleware.RealIP, middleware.Recoverer, middleware.Timeout(30*time.Second))
	r.Get("/healthz", h.Healthz)

	// refueling-stations module
	r.Group(func(r chi.Router) {
		r.Use(gate.Module(tc, "refueling-stations"))
		r.Get("/v1/stations", h.ListStations)
		r.Get("/v1/stations/{id}", h.GetStation)
		r.With(jwtmw.RequireAuth).Post("/v1/stations", h.CreateStation)
		r.With(jwtmw.RequireAuth).Patch("/v1/stations/{id}/status", h.UpdateStationStatus)
	})
	// leak-detection module: incidents + sensor webhook
	r.Group(func(r chi.Router) {
		r.Use(gate.Module(tc, "leak-detection"))
		r.Get("/v1/incidents", h.ListIncidents)
		r.With(jwtmw.RequireAuth).Post("/v1/incidents", h.OpenIncident)
		r.With(jwtmw.RequireAuth).Post("/v1/incidents/{id}/ack", h.AckIncident)
		r.With(jwtmw.RequireAuth).Post("/v1/incidents/{id}/resolve", h.ResolveIncident)
		r.With(leakAuth).Post("/v1/safety/leak", h.IngestLeak)
	})
	// dispatch-workforce module
	r.Group(func(r chi.Router) {
		r.Use(gate.Module(tc, "dispatch-workforce"))
		r.Get("/v1/dispatch/jobs", h.ListDispatchJobs)
		r.With(jwtmw.RequireAuth).Post("/v1/dispatch/jobs", h.CreateDispatchJob)
		r.With(jwtmw.RequireAuth).Post("/v1/dispatch/jobs/{id}/accept", h.AcceptDispatchJob)
	})
	// compliance-reporting module
	r.Group(func(r chi.Router) {
		r.Use(gate.Module(tc, "compliance-reporting"))
		r.Get("/v1/compliance/reports", h.ListComplianceReports)
		r.Get("/v1/compliance/reports/{id}", h.GetComplianceReport)
		r.With(jwtmw.RequireAuth).Post("/v1/compliance/reports/generate", h.GenerateComplianceReport)
	})
	// depot-management module
	r.Group(func(r chi.Router) {
		r.Use(gate.Module(tc, "depot-management"))
		r.Get("/v1/depot/bays", h.ListDepotBays)
		r.Get("/v1/depot/work-orders", h.ListWorkOrders)
		r.With(jwtmw.RequireAuth).Post("/v1/depot/work-orders", h.CreateWorkOrder)
		r.With(jwtmw.RequireAuth).Post("/v1/depot/work-orders/{id}/close", h.CloseWorkOrder)
	})

	srv := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           r,
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		log.Info("infra-api listening", zap.String("addr", srv.Addr))
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
	log.Info("infra-api stopped")
}
