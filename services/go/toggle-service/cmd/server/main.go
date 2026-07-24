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
	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"

	"github.com/munisp/hydrogenTransport/services/go/toggle-service/internal/auth"
	"github.com/munisp/hydrogenTransport/services/go/toggle-service/internal/config"
	"github.com/munisp/hydrogenTransport/services/go/toggle-service/internal/events"
	"github.com/munisp/hydrogenTransport/services/go/toggle-service/internal/handlers"
)

func main() {
	log, _ := zap.NewProduction()
	defer func() { _ = log.Sync() }()

	cfg := config.Load("8080")
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

	var rdb *redis.Client
	if cfg.RedisAddr != "" {
		rdb = redis.NewClient(&redis.Options{Addr: cfg.RedisAddr})
		defer rdb.Close()
	} else {
		log.Warn("REDIS_ADDR not set; running without toggle cache")
	}

	pub := events.NewPublisher(cfg.KafkaBrokers, "toggle-service", log)
	defer pub.Close()

	jwtmw := auth.New(cfg.KeycloakIssuer, log)

	h := handlers.New(pool, rdb, pub, log)
	if err := h.EnsureSchemaAndSeed(ctx); err != nil {
		log.Fatal("ensure schema / seed modules", zap.Error(err))
	}

	r := chi.NewRouter()
	r.Use(middleware.RequestID, middleware.RealIP, middleware.Recoverer, middleware.Timeout(15*time.Second))
	r.Get("/healthz", h.Healthz)

	r.Route("/v1/toggles", func(r chi.Router) {
		r.Get("/", h.List)
		r.Get("/{module}", h.Get)
		r.With(jwtmw.RequireRole("platform-admin")).Put("/{module}", h.Put)
	})

	srv := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           r,
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		log.Info("toggle-service listening", zap.String("addr", srv.Addr))
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
	log.Info("toggle-service stopped")
}
