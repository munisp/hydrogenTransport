// Package dbpool provides the platform-wide tuned Postgres pool and HTTP
// server defaults for the Go services (Wave-11 performance program,
// docs/PERFORMANCE_TUNING.md).
//
// Postgres pool rationale: a bare pgxpool.New runs with MaxConns =
// runtime.NumCPU() and MinConns = 0 — under burst, every request pays a
// fresh connection handshake (TCP + auth + TLS when enabled) on the hot
// path, which is the dominant millisecond-scale jitter source for
// database-backed endpoints. The defaults below keep a warm core of
// connections, cap per-service concurrency to protect Postgres, and recycle
// connections before server-side session limits do.
//
// HTTP server rationale: ReadHeaderTimeout alone leaves the body-read and
// write phases unbounded (slowloris: a client dribbling one byte per minute
// pins a goroutine + connection forever). The values below bound every
// phase while staying far above every legitimate endpoint's latency budget
// (see docs/PERFORMANCE_TUNING.md); middleware.Timeout(30s) still caps
// handler work inside each request.
package dbpool

import (
	"context"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// NewPool opens a tuned pgx pool. Tuning is env-overridable per deployment:
//
//	DB_MAX_CONNS  default 32  (burst capacity; raise for fleet/infra first)
//	DB_MIN_CONNS  default 4   (warm core; pays handshakes only at boot)
func NewPool(ctx context.Context, dsn string) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, err
	}
	cfg.MaxConns = int32(envInt("DB_MAX_CONNS", 32))
	cfg.MinConns = int32(envInt("DB_MIN_CONNS", 4))
	cfg.MaxConnLifetime = 30 * time.Minute // recycle before server-side limits
	cfg.MaxConnIdleTime = 10 * time.Minute // shed genuinely idle connections
	cfg.HealthCheckPeriod = time.Minute    // drop silently-dead conns promptly
	return pgxpool.NewWithConfig(ctx, cfg)
}

// NewServer returns the platform-standard HTTP server: every phase of the
// request lifecycle is bounded (slowloris-safe) while legitimate traffic
// (p99 budget 100 ms per docs/PERFORMANCE_TUNING.md) has orders of
// magnitude of headroom. IdleTimeout aligns with the APISIX upstream
// keepalive (60 s) so the gateway reuses connections instead of re-handshaking.
func NewServer(addr string, handler http.Handler) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       90 * time.Second,
		MaxHeaderBytes:    16 << 10,
	}
}

func envInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return def
}
