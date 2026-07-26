// Package handlers implements the Domain 2 (infrastructure & safety) API.
package handlers

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/zap"

	"github.com/munisp/hydrogenTransport/services/go/infra-api/internal/events"
	"github.com/munisp/hydrogenTransport/services/go/infra-api/internal/workflow"
)

// Handler serves the infra endpoints.
type Handler struct {
	db  DB
	pub events.Publisher
	wf  workflow.Signaler
	log *zap.Logger
}

// New builds a Handler.
func New(db *pgxpool.Pool, pub events.Publisher, wf workflow.Signaler, log *zap.Logger) *Handler {
	return &Handler{db: db, pub: pub, wf: wf, log: log}
}

// EnsureSchema creates the infra schema's supplemental tables owned by this
// service (core tables come from infra/sql migrations).
func (h *Handler) EnsureSchema(ctx context.Context) error {
	stmts := []string{
		`CREATE SCHEMA IF NOT EXISTS infra`,
		`CREATE TABLE IF NOT EXISTS infra.compliance_reports (
			id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
			generated_at timestamptz NOT NULL DEFAULT now(),
			report       jsonb NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS infra.work_orders (
			id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
			title       text NOT NULL,
			description text NOT NULL DEFAULT '',
			asset_ref   text NOT NULL DEFAULT '',
			status      text NOT NULL DEFAULT 'open',
			opened_at   timestamptz NOT NULL DEFAULT now(),
			closed_at   timestamptz
		)`,
		`CREATE TABLE IF NOT EXISTS infra.dispatch_jobs (
			id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
			driver_sub text NOT NULL,
			vehicle_id uuid,
			route      text NOT NULL DEFAULT '',
			starts_at  timestamptz,
			status     text NOT NULL DEFAULT 'assigned',
			created_at timestamptz NOT NULL DEFAULT now()
		)`,
		`ALTER TABLE infra.dispatch_jobs ADD COLUMN IF NOT EXISTS accepted_at timestamptz`,
		// Wave-4 parity with migrations 0005/0007 (dev databases that never
		// ran goose): shift end, work-order linkage/lifecycle columns, station
		// queue, incident resolution timestamp, prediction dedup index.
		`ALTER TABLE infra.dispatch_jobs ADD COLUMN IF NOT EXISTS ends_at timestamptz`,
		`ALTER TABLE infra.work_orders ADD COLUMN IF NOT EXISTS bus_id uuid`,
		`ALTER TABLE infra.work_orders ADD COLUMN IF NOT EXISTS prediction_id uuid`,
		`ALTER TABLE infra.work_orders ADD COLUMN IF NOT EXISTS assignee text`,
		`ALTER TABLE infra.work_orders ADD COLUMN IF NOT EXISTS started_at timestamptz`,
		`ALTER TABLE infra.incidents ADD COLUMN IF NOT EXISTS resolved_at timestamptz`,
		`CREATE TABLE IF NOT EXISTS infra.station_queue (
			id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
			station_id   uuid NOT NULL,
			bus_id       uuid NOT NULL,
			joined_at    timestamptz NOT NULL DEFAULT now(),
			status       text NOT NULL DEFAULT 'waiting',
			completed_at timestamptz
		)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS station_queue_active_uq
			ON infra.station_queue (station_id, bus_id) WHERE status IN ('waiting','serving')`,
		`CREATE UNIQUE INDEX IF NOT EXISTS work_orders_open_prediction_uq
			ON infra.work_orders (prediction_id)
			WHERE prediction_id IS NOT NULL AND status <> 'closed'`,
		`CREATE TABLE IF NOT EXISTS infra.depot_bays (
			id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
			depot       text NOT NULL,
			label       text NOT NULL,
			kind        text NOT NULL DEFAULT 'parking',
			occupied_by uuid,
			status      text NOT NULL DEFAULT 'free',
			UNIQUE (depot, label)
		)`,
		`INSERT INTO infra.depot_bays (depot, label, kind, status) VALUES
			('Riverside Depot', 'F-01', 'fueling', 'free'),
			('Riverside Depot', 'F-02', 'fueling', 'free'),
			('Riverside Depot', 'C-01', 'charging', 'free'),
			('Riverside Depot', 'P-01', 'parking', 'free'),
			('Riverside Depot', 'P-02', 'parking', 'occupied'),
			('Riverside Depot', 'W-01', 'workshop', 'out_of_service')
		ON CONFLICT (depot, label) DO NOTHING`,
	}
	for _, s := range stmts {
		if _, err := h.db.Exec(ctx, s); err != nil {
			return err
		}
	}
	return nil
}

// Healthz reports liveness/readiness.
func (h *Handler) Healthz(w http.ResponseWriter, r *http.Request) {
	if err := h.db.Ping(r.Context()); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "degraded", "postgres": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (h *Handler) internal(w http.ResponseWriter, op string, err error) {
	h.log.Error(op, zap.Error(err))
	writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal_error"})
}

// decodeJSON decodes a JSON request body capped at 1 MiB so unbounded
// payloads cannot exhaust memory (SECURITY_AUDIT F8).
func decodeJSON(w http.ResponseWriter, r *http.Request, v any) error {
	return json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(v)
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
