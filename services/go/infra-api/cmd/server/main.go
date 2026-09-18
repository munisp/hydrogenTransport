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
	"github.com/munisp/hydrogenTransport/services/go/infra-api/internal/consumers"
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

	// maintenance.predicted → depot work orders (predictive-maintenance
	// closed loop, BUSINESS_LOGIC_AUDIT §2). Offsets commit only after the
	// work order is durably written.
	go consumers.StartMaintenanceConsumer(ctx, cfg.KafkaBrokers, pool, log)

	// Optional scheduled compliance generation (COMPLIANCE_REPORT_INTERVAL,
	// e.g. "24h"); manual POST /v1/compliance/reports/generate always works.
	if raw := os.Getenv("COMPLIANCE_REPORT_INTERVAL"); raw != "" {
		if interval, err := time.ParseDuration(raw); err != nil || interval <= 0 {
			log.Warn("invalid COMPLIANCE_REPORT_INTERVAL; scheduled generation disabled", zap.String("value", raw))
		} else {
			go func() {
				ticker := time.NewTicker(interval)
				defer ticker.Stop()
				for {
					select {
					case <-ctx.Done():
						return
					case <-ticker.C:
						if err := h.GenerateScheduled(ctx); err != nil {
							log.Error("scheduled compliance report failed", zap.Error(err))
						} else {
							log.Info("scheduled compliance report generated", zap.Duration("interval", interval))
						}
					}
				}
			}()
			log.Info("scheduled compliance generation enabled", zap.Duration("interval", interval))
		}
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
		// Wave-8 W8-9: station-staff is now a real realm role (no longer
		// mapped to operator at onboarding); station operations accept both.
		r.With(jwtmw.RequireAnyRole("operator", "station-staff")).Post("/v1/stations", h.CreateStation)
		r.With(jwtmw.RequireAnyRole("operator", "station-staff")).Patch("/v1/stations/{id}/status", h.UpdateStationStatus)
		// OCPP charger read APIs (Wave 5; infra.charge_points/charging_sessions
		// are written by the ocpp-gateway).
		r.Get("/v1/stations/{id}/chargers", h.ListStationChargers)
		r.Get("/v1/chargers", h.ListChargers)
		r.Get("/v1/chargers/{ocpp_id}/sessions", h.ListChargerSessions)
		// Queue management (SPEC §1 "queue mgmt").
		r.Get("/v1/stations/{id}/queue", h.ListStationQueue)
		r.With(jwtmw.RequireAuth).Post("/v1/stations/{id}/queue", h.JoinStationQueue)
		r.With(jwtmw.RequireAnyRole("operator", "station-staff")).Post("/v1/stations/{id}/queue/{entry}/complete", h.CompleteStationQueueEntry)
		r.With(jwtmw.RequireAuth).Post("/v1/stations/{id}/queue/{entry}/leave", h.LeaveStationQueue)
	})
	// leak-detection module: incidents + sensor webhook
	r.Group(func(r chi.Router) {
		r.Use(gate.Module(tc, "leak-detection"))
		// The incident feed contains safety/PII data; citizens must not list
		// all incidents (SECURITY_AUDIT F12 / task: incident list gating).
		// station-staff is accepted as a role name even though today it maps
		// to the operator realm role at provisioning time.
		r.With(jwtmw.RequireAnyRole("operator", "platform-admin", "station-staff", "auditor")).
			Get("/v1/incidents", h.ListIncidents)
		r.With(jwtmw.RequireAuth).Post("/v1/incidents", h.OpenIncident)
		r.With(jwtmw.RequireRole("operator")).Post("/v1/incidents/{id}/ack", h.AckIncident)
		r.With(jwtmw.RequireRole("operator")).Post("/v1/incidents/{id}/resolve", h.ResolveIncident)
		r.With(leakAuth).Post("/v1/safety/leak", h.IngestLeak)
		// Wave-6 A4-01: driver panic channel.
		r.With(jwtmw.RequireRole("driver")).Post("/v1/safety/sos", h.TriggerSOS)
		// Wave-6 A4-04: chain-of-custody evidence export (regulator/insurer).
		r.With(jwtmw.RequireAnyRole("platform-admin", "auditor")).
			Get("/v1/incidents/{id}/evidence-pack", h.GetEvidencePack)
		// Wave-6 A3-03: outbound partner webhooks.
		r.With(jwtmw.RequireRole("platform-admin")).Post("/v1/webhooks/subscriptions", h.CreateWebhookSubscription)
		r.With(jwtmw.RequireRole("platform-admin")).Get("/v1/webhooks/subscriptions", h.ListWebhookSubscriptions)
		r.With(jwtmw.RequireRole("platform-admin")).Delete("/v1/webhooks/subscriptions/{id}", h.DeactivateWebhookSubscription)
		r.With(jwtmw.RequireAnyRole("operator", "platform-admin")).Post("/v1/webhooks/deliveries/{id}/retry", h.RetryWebhookDelivery)
	})
	// dispatch-workforce module
	r.Group(func(r chi.Router) {
		r.Use(gate.Module(tc, "dispatch-workforce"))
		// Wave-9 W9-3: driver registration closes the onboarding loop — an
		// approved driver self-registers (sub from JWT) or an operator
		// registers them; without an infra.drivers row no job can be assigned.
		r.With(jwtmw.RequireRole("driver")).Post("/v1/drivers/register", h.RegisterDriver)
		r.With(jwtmw.RequireRole("operator")).Post("/v1/drivers", h.CreateDriver)
		// Wave-10 W10-2: driver offboarding/lifecycle (suspend immediately
		// blocks job acceptance).
		r.With(jwtmw.RequireRole("operator")).Post("/v1/drivers/{sub}/status", h.SetDriverStatus)
		r.Get("/v1/dispatch/jobs", h.ListDispatchJobs)
		r.With(jwtmw.RequireRole("operator")).Post("/v1/dispatch/jobs", h.CreateDispatchJob)
		r.With(jwtmw.RequireRole("driver")).Post("/v1/dispatch/jobs/{id}/accept", h.AcceptDispatchJob)
		r.With(jwtmw.RequireRole("operator")).Post("/v1/dispatch/jobs/{id}/cancel", h.CancelDispatchJob)
		// Wave-6 A1-05: mid-shift vehicle breakdown swap.
		r.With(jwtmw.RequireRole("operator")).Post("/v1/dispatch/jobs/{id}/swap-vehicle", h.SwapDispatchVehicle)
		// Wave-7 A2-09: charter/school block bookings — multi-vehicle
		// reservations backed by dispatch jobs (route='charter:<reference>').
		r.With(jwtmw.RequireRole("operator")).Post("/v1/charters", h.CreateCharter)
		r.With(jwtmw.RequireRole("operator")).Get("/v1/charters", h.ListCharters)
		r.With(jwtmw.RequireRole("operator")).Get("/v1/charters/{id}", h.GetCharter)
		r.With(jwtmw.RequireRole("operator")).Post("/v1/charters/{id}/cancel", h.CancelCharter)
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
		r.With(jwtmw.RequireRole("operator")).Post("/v1/depot/bays/{id}/assign", h.AssignDepotBay)
		r.With(jwtmw.RequireRole("operator")).Post("/v1/depot/bays/{id}/release", h.ReleaseDepotBay)
		r.Get("/v1/depot/work-orders", h.ListWorkOrders)
		r.With(jwtmw.RequireRole("operator")).Post("/v1/depot/work-orders", h.CreateWorkOrder)
		r.With(jwtmw.RequireRole("operator")).Post("/v1/depot/work-orders/{id}/assign", h.AssignWorkOrder)
		r.With(jwtmw.RequireRole("operator")).Post("/v1/depot/work-orders/{id}/start", h.StartWorkOrder)
		r.With(jwtmw.RequireRole("operator")).Post("/v1/depot/work-orders/{id}/hold", h.HoldWorkOrder)
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
