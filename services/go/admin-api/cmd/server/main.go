// admin-api — Admin & Onboarding backend for H2Fleet. Port 8085
// (gateway prefix /api/admin/*). See README.md for the full contract.
package main

import (
	"context"
	"errors"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/zap"

	auth "github.com/munisp/hydrogenTransport/packages/go-auth"
	"github.com/munisp/hydrogenTransport/services/go/admin-api/internal/config"
	"github.com/munisp/hydrogenTransport/services/go/admin-api/internal/httpx"
	"github.com/munisp/hydrogenTransport/services/go/admin-api/internal/keycloak"
	"github.com/munisp/hydrogenTransport/services/go/admin-api/internal/kpi"
	"github.com/munisp/hydrogenTransport/services/go/admin-api/internal/onboarding"
	"github.com/munisp/hydrogenTransport/services/go/admin-api/internal/ops"
	"github.com/munisp/hydrogenTransport/services/go/admin-api/internal/server"
	"github.com/munisp/hydrogenTransport/services/go/admin-api/internal/users"
	"github.com/munisp/hydrogenTransport/services/go/audit-log/pkg/auditclient"
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

	store := onboarding.NewPGStore(pool)
	if err := store.EnsureSchema(ctx); err != nil {
		log.Fatal("ensure platform schema", zap.Error(err))
	}

	kc := keycloak.New(cfg.KeycloakAdminURL, cfg.KeycloakRealm,
		cfg.KeycloakAdminClientID, cfg.KeycloakAdminClientSecret, log)
	jwtmw := auth.New(cfg.KeycloakIssuer, log)

	agg := kpi.NewAggregator([]kpi.Source{
		kpi.FleetSource(pool),
		kpi.InfraSource(pool),
		kpi.CitizenSource(pool),
		kpi.CommerceSource(pool),
		kpi.ToggleSource(cfg.ToggleURL),
	}, kpi.DefaultTimeout)

	opsHandler := ops.NewHandler(healthTargets(cfg), cfg.AlertmanagerURL, cfg.ToggleURL, log)

	router := server.NewRouter(server.Deps{
		Log: log,
		JWT: jwtmw,
		Healthz: func(w http.ResponseWriter, r *http.Request) {
			pctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
			defer cancel()
			if err := store.Ping(pctx); err != nil {
				httpx.JSON(w, http.StatusServiceUnavailable, map[string]string{"status": "unhealthy"})
				return
			}
			httpx.JSON(w, http.StatusOK, map[string]string{"status": "ok"})
		},
		Onboarding: onboarding.NewHandler(store, kc, log, nil),
		Users:      users.NewHandler(kc, log),
		KPIs:       agg,
		Ops:        opsHandler,
		// Insider-threat audit emission (docs/INSIDER_THREAT.md). Disabled
		// (noop) unless AUDIT_LOG_URL is set.
		Audit: auditclient.FromEnv("admin-api", log, os.Getenv),
	})

	srv := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           router,
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		log.Info("admin-api listening", zap.String("addr", srv.Addr))
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
	log.Info("admin-api stopped")
}

// healthTargets builds the NOC/SOC sweep list: all 10 platform services
// (HTTP /healthz) plus the middleware TCP endpoints.
func healthTargets(cfg config.Config) []ops.Target {
	return []ops.Target{
		{Name: "toggle-service", Kind: "http", Addr: cfg.ToggleServiceURL},
		{Name: "fleet-api", Kind: "http", Addr: cfg.FleetAPIURL},
		{Name: "infra-api", Kind: "http", Addr: cfg.InfraAPIURL},
		{Name: "citizen-api", Kind: "http", Addr: cfg.CitizenAPIURL},
		{Name: "commerce-api", Kind: "http", Addr: cfg.CommerceAPIURL},
		{Name: "admin-api", Kind: "http", Addr: cfg.AdminAPIURL},
		{Name: "predictive-maintenance", Kind: "http", Addr: cfg.PredictiveURL},
		{Name: "route-optimizer", Kind: "http", Addr: cfg.OptimizerURL},
		{Name: "digital-twin", Kind: "http", Addr: cfg.DigitalTwinURL},
		{Name: "carbon-analytics", Kind: "http", Addr: cfg.CarbonURL},

		{Name: "kafka", Kind: "tcp", Addr: cfg.KafkaAddr},
		{Name: "postgres", Kind: "tcp", Addr: cfg.PostgresAddr},
		{Name: "redis", Kind: "tcp", Addr: cfg.RedisAddr},
		{Name: "opensearch", Kind: "tcp", Addr: cfg.OpenSearchAddr},
		{Name: "temporal", Kind: "tcp", Addr: cfg.TemporalAddr},
		{Name: "tigerbeetle", Kind: "tcp", Addr: cfg.TigerBeetleAddr},
	}
}
