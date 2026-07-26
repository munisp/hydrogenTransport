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
	toggle "github.com/munisp/hydrogenTransport/packages/toggle-client/go"
	"github.com/munisp/hydrogenTransport/services/go/citizen-api/internal/config"
	"github.com/munisp/hydrogenTransport/services/go/citizen-api/internal/consumers"
	"github.com/munisp/hydrogenTransport/services/go/citizen-api/internal/gate"
	"github.com/munisp/hydrogenTransport/services/go/citizen-api/internal/handlers"
	"github.com/munisp/hydrogenTransport/services/go/citizen-api/internal/metrics"
	"github.com/munisp/hydrogenTransport/services/go/citizen-api/internal/pubsub"
)

func main() {
	log, _ := zap.NewProduction()
	defer func() { _ = log.Sync() }()

	cfg := config.Load("8083")
	if cfg.ToggleURL == "" {
		log.Warn("TOGGLE_URL not set; toggle client is fail-closed so all module routes will 404")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// The passenger (GTFS seed) endpoints work without a database; DRT and
	// carbon endpoints need Postgres and return 503 when it is not configured.
	var pool *pgxpool.Pool
	if cfg.DatabaseURL != "" {
		var err error
		pool, err = pgxpool.New(ctx, cfg.DatabaseURL)
		if err != nil {
			log.Fatal("connect postgres", zap.Error(err))
		}
		defer pool.Close()
		if err := pool.Ping(ctx); err != nil {
			log.Fatal("ping postgres", zap.Error(err))
		}
	} else {
		log.Warn("DATABASE_URL not set; DRT and carbon endpoints will return 503")
	}

	tc := toggle.New(cfg.ToggleURL)
	jwtmw := auth.New(cfg.KeycloakIssuer, log)
	pub := pubsub.New(log)
	defer pub.Close()
	h := handlers.New(pool, pub, tc, log)
	if pool != nil {
		// drt.requested → nearest-vehicle auto-assignment (BUSINESS_LOGIC_AUDIT
		// §13: the event was produced-never-consumed and assigned/enroute/
		// completed were unreachable).
		go consumers.StartDRTConsumer(ctx, cfg.KafkaBrokers, pool, pub, log)
	}
	od := handlers.NewOpenData(os.Getenv("OPENSEARCH_URL"), os.Getenv("OPENSEARCH_INDEX"), log)

	r := chi.NewRouter()
	r.Use(middleware.RequestID, middleware.RealIP, middleware.Recoverer, metrics.Middleware("citizen-api"), middleware.Timeout(30*time.Second))
	r.Get("/healthz", h.Healthz)
	r.Handle("/metrics", metrics.Handler())

	// passenger-pwa module: GTFS-static style seed data + service alerts
	r.Group(func(r chi.Router) {
		r.Use(gate.Module(tc, "passenger-pwa"))
		r.Get("/v1/passenger/stops", h.ListStops)
		r.Get("/v1/passenger/routes", h.ListRoutes)
		r.Get("/v1/passenger/arrivals", h.GetArrivals)
		r.Get("/v1/passenger/journey", h.PlanJourney)
		r.Get("/v1/passenger/alerts", h.ListAlerts)
	})
	// mobile-app module: bootstrap config for the Expo apps
	r.With(gate.Module(tc, "mobile-app")).Get("/v1/mobile/config", h.MobileConfig)
	// demand-responsive module
	r.Group(func(r chi.Router) {
		r.Use(gate.Module(tc, "demand-responsive"))
		r.With(jwtmw.RequireAuth).Post("/v1/drt/requests", h.CreateDRTRequest)
		r.With(jwtmw.RequireAuth).Get("/v1/drt/requests", h.ListDRTRequests)
		r.With(jwtmw.RequireAuth).Get("/v1/drt/requests/{id}", h.GetDRTRequest)
		r.With(jwtmw.RequireAuth).Post("/v1/drt/requests/{id}/cancel", h.CancelDRTRequest)
		r.With(jwtmw.RequireRole("operator")).Post("/v1/drt/requests/{id}/assign", h.AssignDRTRequest)
		r.With(jwtmw.RequireAnyRole("driver", "operator")).Post("/v1/drt/requests/{id}/start", h.ProgressDRTRequest)
		r.With(jwtmw.RequireAnyRole("driver", "operator")).Post("/v1/drt/requests/{id}/complete", h.ProgressDRTRequest)
	})
	// carbon-credits module
	r.Group(func(r chi.Router) {
		r.Use(gate.Module(tc, "carbon-credits"))
		r.Get("/v1/carbon/credits", h.ListCarbonCredits)
		r.Get("/v1/carbon/credits/summary", h.CarbonSummary)
	})
	// open-data-portal module
	r.Group(func(r chi.Router) {
		r.Use(gate.Module(tc, "open-data-portal"))
		r.Get("/v1/opendata/datasets", od.ListDatasets)
		r.Get("/v1/opendata/search", od.Search)
		r.Get("/v1/opendata/gtfs", h.GTFSZip)
		r.Get("/v1/opendata/gtfs/{file}", h.GTFSFile)
	})

	srv := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           r,
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		log.Info("citizen-api listening", zap.String("addr", srv.Addr))
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
	log.Info("citizen-api stopped")
}
