// Package handlers implements the feature-toggle REST API (SPEC §3.2).
package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"

	"github.com/munisp/hydrogenTransport/services/go/toggle-service/internal/events"
)

// cacheTTL is the Redis cache TTL for toggles:<module> keys (SPEC §3.2).
const cacheTTL = 30 * time.Second

// Modules maps every module identifier (SPEC §3.1) to its domain.
var Modules = map[string]string{
	// Domain 1 — fleet
	"telematics":             "fleet",
	"predictive-maintenance": "fleet",
	"digital-twin":           "fleet",
	"fuel-monitoring":        "fleet",
	"route-energy-optimizer": "fleet",
	// Domain 2 — infra
	"refueling-stations":   "infra",
	"leak-detection":       "infra",
	"dispatch-workforce":   "infra",
	"compliance-reporting": "infra",
	"depot-management":     "infra",
	// Domain 3 — citizen
	"passenger-pwa":     "citizen",
	"mobile-app":        "citizen",
	"demand-responsive": "citizen",
	"carbon-credits":    "citizen",
	"open-data-portal":  "citizen",
	// Domain 4 — commerce
	"fare-payments":       "commerce",
	"loyalty-marketplace": "commerce",
	"energy-trading":      "commerce",
	"gov-dashboard":       "commerce",
	"advertising":         "commerce",
}

// Handler serves the toggle endpoints.
type Handler struct {
	db  *pgxpool.Pool
	rdb *redis.Client // may be nil (cache disabled)
	pub events.Publisher
	log *zap.Logger
}

// New builds a Handler.
func New(db *pgxpool.Pool, rdb *redis.Client, pub events.Publisher, log *zap.Logger) *Handler {
	return &Handler{db: db, rdb: rdb, pub: pub, log: log}
}

// EnsureSchemaAndSeed creates feature_toggles if needed and seeds all 20
// modules idempotently (INSERT ... ON CONFLICT DO NOTHING, SPEC §3.2).
func (h *Handler) EnsureSchemaAndSeed(ctx context.Context) error {
	if _, err := h.db.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS feature_toggles (
			module     text PRIMARY KEY,
			domain     text NOT NULL,
			enabled    boolean NOT NULL DEFAULT true,
			updated_at timestamptz NOT NULL DEFAULT now()
		)`); err != nil {
		return err
	}
	tx, err := h.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	for module, domain := range Modules {
		if _, err := tx.Exec(ctx,
			`INSERT INTO feature_toggles (module, domain, enabled) VALUES ($1, $2, true)
			 ON CONFLICT (module) DO NOTHING`, module, domain); err != nil {
			return err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	h.log.Info("seeded feature toggles", zap.Int("modules", len(Modules)))
	return nil
}

// Healthz reports liveness/readiness including Postgres (and Redis when configured).
func (h *Handler) Healthz(w http.ResponseWriter, r *http.Request) {
	status := map[string]string{"status": "ok"}
	code := http.StatusOK
	if err := h.db.Ping(r.Context()); err != nil {
		status["status"] = "degraded"
		status["postgres"] = err.Error()
		code = http.StatusServiceUnavailable
	}
	if h.rdb != nil {
		if err := h.rdb.Ping(r.Context()).Err(); err != nil {
			status["status"] = "degraded"
			status["redis"] = err.Error()
			code = http.StatusServiceUnavailable
		}
	}
	writeJSON(w, code, status)
}

// List handles GET /v1/toggles → { "toggles": { "<module>": bool, ... } }.
func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	rows, err := h.db.Query(r.Context(), `SELECT module, enabled FROM feature_toggles ORDER BY module`)
	if err != nil {
		h.internal(w, "query toggles", err)
		return
	}
	defer rows.Close()
	toggles := map[string]bool{}
	for rows.Next() {
		var module string
		var enabled bool
		if err := rows.Scan(&module, &enabled); err != nil {
			h.internal(w, "scan toggle", err)
			return
		}
		toggles[module] = enabled
	}
	if err := rows.Err(); err != nil {
		h.internal(w, "iterate toggles", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"toggles": toggles})
}

// Get handles GET /v1/toggles/{module} → { module, enabled, domain }.
// Reads through the Redis cache (toggles:<module>, TTL 30s).
func (h *Handler) Get(w http.ResponseWriter, r *http.Request) {
	module := chi.URLParam(r, "module")
	if _, known := Modules[module]; !known {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown module"})
		return
	}

	if h.rdb != nil {
		if cached, err := h.rdb.Get(r.Context(), cacheKey(module)).Result(); err == nil {
			writeJSON(w, http.StatusOK, map[string]any{
				"module":  module,
				"enabled": cached == "true",
				"domain":  Modules[module],
			})
			return
		} else if !errors.Is(err, redis.Nil) {
			h.log.Warn("redis cache read failed", zap.String("module", module), zap.Error(err))
		}
	}

	var enabled bool
	var domain string
	err := h.db.QueryRow(r.Context(),
		`SELECT enabled, domain FROM feature_toggles WHERE module = $1`, module).Scan(&enabled, &domain)
	if errors.Is(err, pgx.ErrNoRows) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown module"})
		return
	}
	if err != nil {
		h.internal(w, "query toggle", err)
		return
	}

	if h.rdb != nil {
		if err := h.rdb.Set(r.Context(), cacheKey(module), boolString(enabled), cacheTTL).Err(); err != nil {
			h.log.Warn("redis cache write failed", zap.String("module", module), zap.Error(err))
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"module": module, "enabled": enabled, "domain": domain})
}

type putRequest struct {
	Enabled *bool `json:"enabled"`
}

// Put handles PUT /v1/toggles/{module} (platform-admin only; enforced by middleware).
// Updates Postgres, refreshes the Redis cache and publishes toggle.changed (SPEC §3.2/§3.3).
func (h *Handler) Put(w http.ResponseWriter, r *http.Request) {
	module := chi.URLParam(r, "module")
	domain, known := Modules[module]
	if !known {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown module"})
		return
	}
	var req putRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Enabled == nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "body must be {\"enabled\": bool}"})
		return
	}

	res, err := h.db.Exec(r.Context(),
		`UPDATE feature_toggles SET enabled = $2, updated_at = now() WHERE module = $1`,
		module, *req.Enabled)
	if err != nil {
		h.internal(w, "update toggle", err)
		return
	}
	if res.RowsAffected() == 0 {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown module"})
		return
	}

	if h.rdb != nil {
		if err := h.rdb.Set(r.Context(), cacheKey(module), boolString(*req.Enabled), cacheTTL).Err(); err != nil {
			h.log.Warn("redis cache refresh failed", zap.String("module", module), zap.Error(err))
		}
	}

	if err := h.pub.Publish(r.Context(), "toggle.changed", map[string]any{
		"module":  module,
		"enabled": *req.Enabled,
		"domain":  domain,
	}); err != nil {
		// State is already persisted; the 30s Redis TTL bounds staleness.
		h.log.Error("failed to publish toggle.changed", zap.String("module", module), zap.Error(err))
	}

	h.log.Info("toggle changed",
		zap.String("module", module), zap.Bool("enabled", *req.Enabled))
	writeJSON(w, http.StatusOK, map[string]any{"module": module, "enabled": *req.Enabled, "domain": domain})
}

func cacheKey(module string) string { return "toggles:" + module }

func boolString(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

func (h *Handler) internal(w http.ResponseWriter, op string, err error) {
	h.log.Error(op, zap.Error(err))
	writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
