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
	"github.com/munisp/hydrogenTransport/services/go/commerce-api/internal/auth"
	"github.com/munisp/hydrogenTransport/services/go/commerce-api/internal/config"
	"github.com/munisp/hydrogenTransport/services/go/commerce-api/internal/events"
	"github.com/munisp/hydrogenTransport/services/go/commerce-api/internal/gate"
	"github.com/munisp/hydrogenTransport/services/go/commerce-api/internal/handlers"
	"github.com/munisp/hydrogenTransport/services/go/commerce-api/internal/ledger"
)

func main() {
	log, _ := zap.NewProduction()
	defer func() { _ = log.Sync() }()

	cfg := config.Load("8084")
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

	led, err := ledger.New(os.Getenv("TIGERBEETLE_ADDR"), log)
	if err != nil {
		log.Fatal("ledger init", zap.Error(err))
	}
	defer led.Close()

	tc := toggle.New(cfg.ToggleURL)
	jwtmw := auth.New(cfg.KeycloakIssuer, log)
	pub := events.NewPublisher(cfg.KafkaBrokers, "commerce-api", log)
	defer pub.Close()

	h := handlers.New(pool, led, pub, log)
	if err := h.EnsureSchema(ctx); err != nil {
		log.Fatal("ensure schema", zap.Error(err))
	}

	r := chi.NewRouter()
	r.Use(middleware.RequestID, middleware.RealIP, middleware.Recoverer, middleware.Timeout(30*time.Second))
	r.Get("/healthz", h.Healthz)

	// fare-payments module
	r.Group(func(r chi.Router) {
		r.Use(gate.Module(tc, "fare-payments"))
		r.With(jwtmw.RequireAuth).Post("/v1/payments", h.CreatePayment(os.Getenv("MOJALOOP_ENDPOINT")))
		r.Get("/v1/payments", h.ListPayments)
		r.Get("/v1/payments/{id}", h.GetPayment)
	})
	// loyalty-marketplace module
	r.Group(func(r chi.Router) {
		r.Use(gate.Module(tc, "loyalty-marketplace"))
		r.With(jwtmw.RequireAuth).Get("/v1/loyalty/balance", h.GetLoyaltyBalance)
		r.With(jwtmw.RequireAuth).Post("/v1/loyalty/redeem", h.RedeemOffer)
		r.Get("/v1/marketplace/offers", h.ListOffers)
		r.With(jwtmw.RequireAuth).Post("/v1/marketplace/offers", h.CreateOffer)
	})
	// energy-trading module
	r.Group(func(r chi.Router) {
		r.Use(gate.Module(tc, "energy-trading"))
		r.Get("/v1/energy/trades", h.ListTrades)
		r.With(jwtmw.RequireAuth).Post("/v1/energy/trades", h.CreateTrade)
	})
	// gov-dashboard module
	r.With(gate.Module(tc, "gov-dashboard")).Get("/v1/gov/kpis", h.GetGovKPIs)
	// advertising module
	r.Group(func(r chi.Router) {
		r.Use(gate.Module(tc, "advertising"))
		r.Get("/v1/ads/campaigns", h.ListCampaigns)
		r.Get("/v1/ads/campaigns/{id}", h.GetCampaign)
		r.With(jwtmw.RequireAuth).Post("/v1/ads/campaigns", h.CreateCampaign)
		r.With(jwtmw.RequireAuth).Patch("/v1/ads/campaigns/{id}", h.UpdateCampaign)
	})

	srv := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           r,
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		log.Info("commerce-api listening", zap.String("addr", srv.Addr))
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
	log.Info("commerce-api stopped")
}
