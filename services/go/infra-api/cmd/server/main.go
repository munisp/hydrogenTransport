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

	auth "github.com/munisp/hydrogenTransport/packages/go-auth"
	toggle "github.com/munisp/hydrogenTransport/packages/toggle-client/go"
	"github.com/munisp/hydrogenTransport/services/go/infra-api/internal/config"
	"github.com/munisp/hydrogenTransport/services/go/infra-api/internal/events"
	"github.com/munisp/hydrogenTransport/services/go/infra-api/internal/gate"
	"github.com/munisp/hydrogenTransport/services/go/infra-api/internal/handlers"
	"github.com/munisp/hydrogenTransport/services/go/infra-api/internal/metrics"
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
	// Permify ReBAC checks on admin routes (SPEC §3.5). When PERMIFY_GRPC is
	// unset the checks fall back to role-only (warned once); see packages/go-auth.
	perm := auth.NewPermify(os.Getenv("PERMIFY_GRPC"), os.Getenv("PERMIFY_TENANT"), log)
	defer perm.Close()
	pub := events.NewPublisher(cfg.KafkaBrokers, "infra-api", log)
	defer pub.Close()
	temporalHost := os.Getenv("TEMPORAL_HOST")
	wf := workflow.NewSignaler(temporalHost, log)
	defer wf.Close()
	// Temporal worker for the incident-response and dispatch workflows
	// (SPEC §3.8). Skipped gracefully when TEMPORAL_HOST is unset or the
	// server is unreachable — the HTTP API still serves either way.
	stopWorker, err := workflow.StartWorker(temporalHost, pool, log)
	if err != nil {
		log.Warn("temporal worker not started; continuing without workflows", zap.Error(err))
	} else {
		defer stopWorker()
	}

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
	r.Use(middleware.RequestID, middleware.RealIP, middleware.Recoverer, metrics.Middleware("infra-api"), middleware.Timeout(30*time.Second))
	r.Get("/healthz", h.Healthz)
	r.Handle("/metrics", metrics.Handler())

	// refueling-stations module
	r.Group(func(r chi.Router) {
		r.Use(gate.Module(tc, "refueling-stations"))
		r.Get("/v1/stations", h.ListStations)
		r.Get("/v1/stations/{id}", h.GetStation)
		r.With(jwtmw.RequireRole("operator")).Post("/v1/stations", h.CreateStation)
		r.With(jwtmw.RequireRole("operator")).Patch("/v1/stations/{id}/status", h.UpdateStationStatus)
	})
	// leak-detection module: incidents + sensor webhook
	r.Group(func(r chi.Router) {
		r.Use(gate.Module(tc, "leak-detection"))
		// The incident feed contains safety/PII data; citizens must not list
		// all incidents (SECURITY_AUDIT F12 / task: incident list gating).
		// station-staff is accepted as a role name even though today it maps
		// to the operator realm role at provisioning time.
		r.With(jwtmw.RequireAnyRole("operator", "platform-admin", "station-staff")).
			Get("/v1/incidents", h.ListIncidents)
		r.With(jwtmw.RequireAuth).Post("/v1/incidents", h.OpenIncident)
		r.With(jwtmw.RequireRole("operator")).Post("/v1/incidents/{id}/ack", h.AckIncident)
		r.With(jwtmw.RequireRole("operator")).Post("/v1/incidents/{id}/resolve", h.ResolveIncident)
		r.With(leakAuth).Post("/v1/safety/leak", h.IngestLeak)
	})
	// dispatch-workforce module
	r.Group(func(r chi.Router) {
		r.Use(gate.Module(tc, "dispatch-workforce"))
		r.Get("/v1/dispatch/jobs", h.ListDispatchJobs)
		r.With(jwtmw.RequireRole("operator")).Post("/v1/dispatch/jobs", h.CreateDispatchJob)
		r.With(jwtmw.RequireRole("driver")).Post("/v1/dispatch/jobs/{id}/accept", h.AcceptDispatchJob)
	})
	// compliance-reporting module
	r.Group(func(r chi.Router) {
		r.Use(gate.Module(tc, "compliance-reporting"))
		r.Get("/v1/compliance/reports", h.ListComplianceReports)
		r.Get("/v1/compliance/reports/{id}", h.GetComplianceReport)
		r.With(
			jwtmw.RequireRole("operator"),
			perm.Require("report", "generate", func(*http.Request) string { return "compliance" }),
		).Post("/v1/compliance/reports/generate", h.GenerateComplianceReport)
	})
	// depot-management module
	r.Group(func(r chi.Router) {
		r.Use(gate.Module(tc, "depot-management"))
		r.Get("/v1/depot/bays", h.ListDepotBays)
		r.Get("/v1/depot/work-orders", h.ListWorkOrders)
		r.With(jwtmw.RequireRole("operator")).Post("/v1/depot/work-orders", h.CreateWorkOrder)
		r.With(jwtmw.RequireRole("operator")).Post("/v1/depot/work-orders/{id}/close", h.CloseWorkOrder)
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
