// admin-api — Admin & Onboarding backend for H2Fleet. Port 8085
// (gateway prefix /api/admin/*). See README.md for the full contract.
package main

import (
	"context"
	"errors"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"go.uber.org/zap"

	auth "github.com/munisp/hydrogenTransport/packages/go-auth"
	dbpool "github.com/munisp/hydrogenTransport/packages/go-db"
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

	pool, err := dbpool.NewPool(ctx, cfg.DatabaseURL)
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

	kc, err := keycloak.New(cfg.KeycloakAdminURL, cfg.KeycloakRealm,
		cfg.KeycloakAdminClientID, cfg.KeycloakAdminClientSecret, log)
	if err != nil {
		log.Fatal("keycloak admin client init", zap.Error(err))
	}
	jwtmw := auth.New(cfg.KeycloakIssuer, log)

	agg := kpi.NewAggregator([]kpi.Source{
		kpi.FleetSource(pool),
		kpi.InfraSource(pool),
		kpi.CitizenSource(pool),
		kpi.CommerceSource(pool),
		kpi.ToggleSource(cfg.ToggleURL),
	}, kpi.DefaultTimeout)

	opsHandler := ops.NewHandler(healthTargets(cfg), cfg.AlertmanagerURL, cfg.ToggleURL, log)

	obHandler := onboarding.NewHandler(store, kc, log, nil)

	// Wave-8 W8-7: pending-request TTL (default 30 days). A TTL guard on the
	// decide path expires stale requests on the spot; this sweep (startup +
	// daily) expires them in bulk so they also surface as expired in lists.
	pendingTTLDays := 30
	if raw := os.Getenv("ONBOARDING_PENDING_TTL_DAYS"); raw != "" {
		if n, err := strconv.Atoi(raw); err != nil || n < 1 {
			log.Warn("invalid ONBOARDING_PENDING_TTL_DAYS; using default 30", zap.String("value", raw))
		} else {
			pendingTTLDays = n
		}
	}
	obHandler.PendingTTL = time.Duration(pendingTTLDays) * 24 * time.Hour
	expireSweep := func() {
		sctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		n, err := store.ExpirePending(sctx, time.Now().Add(-obHandler.PendingTTL))
		if err != nil {
			log.Error("onboarding pending-TTL sweep failed", zap.Error(err))
		} else if n > 0 {
			log.Info("onboarding pending requests expired", zap.Int64("count", n))
		}
	}
	expireSweep()
	go func() {
		ticker := time.NewTicker(24 * time.Hour)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				expireSweep()
			}
		}
	}()

	// Wave-8 W8-8: optional captcha on the public intake endpoints
	// (siteverify-compatible provider, e.g. hCaptcha/Turnstile). Both vars
	// must be set; unset = disabled (dev default). Fail-closed when set.
	if verifyURL, secret := os.Getenv("ONBOARDING_CAPTCHA_VERIFY_URL"), os.Getenv("ONBOARDING_CAPTCHA_SECRET"); verifyURL != "" && secret != "" {
		obHandler.Captcha = &onboarding.CaptchaConfig{VerifyURL: verifyURL, Secret: secret}
		log.Info("onboarding captcha verification enabled", zap.String("verify_url", verifyURL))
	}

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
		Onboarding: obHandler,
		Users:      users.NewHandler(kc, log),
		KPIs:       agg,
		Ops:        opsHandler,
		// Insider-threat audit emission (docs/INSIDER_THREAT.md). Disabled
		// (noop) unless AUDIT_LOG_URL is set.
		Audit: auditclient.FromEnv("admin-api", log, os.Getenv),
	})

	srv := dbpool.NewServer(":"+cfg.Port, router)

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
