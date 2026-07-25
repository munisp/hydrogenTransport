// Package handlers implements the Domain 4 (commerce & finance) API.
package handlers

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/zap"

	"github.com/munisp/hydrogenTransport/services/go/commerce-api/internal/events"
	"github.com/munisp/hydrogenTransport/services/go/commerce-api/internal/ledger"
)

// Handler serves the commerce endpoints.
type Handler struct {
	db     DB
	ledger ledger.Ledger
	pub    events.Publisher
	log    *zap.Logger
}

// New builds a Handler.
func New(db *pgxpool.Pool, led ledger.Ledger, pub events.Publisher, log *zap.Logger) *Handler {
	return &Handler{db: db, ledger: led, pub: pub, log: log}
}

// EnsureSchema creates supplemental commerce tables owned by this service and
// adds the idempotency-key column to commerce.fare_payments (core tables come
// from infra/sql migrations; the ALTER is additive and idempotent).
func (h *Handler) EnsureSchema(ctx context.Context) error {
	stmts := []string{
		`CREATE SCHEMA IF NOT EXISTS commerce`,
		`ALTER TABLE commerce.fare_payments ADD COLUMN IF NOT EXISTS idempotency_key text`,
		`ALTER TABLE commerce.fare_payments ADD COLUMN IF NOT EXISTS tb_transfer_id text`,
		`CREATE UNIQUE INDEX IF NOT EXISTS fare_payments_idempotency_key_uq
			ON commerce.fare_payments (idempotency_key) WHERE idempotency_key IS NOT NULL`,
		// Trades: tb_transfer_id mirrors migration 0005 (S10); the
		// idempotency key follows the fare_payments precedent (runtime DDL
		// until a later migration absorbs it).
		`ALTER TABLE commerce.trades ADD COLUMN IF NOT EXISTS tb_transfer_id text`,
		`ALTER TABLE commerce.trades ADD COLUMN IF NOT EXISTS idempotency_key text`,
		`CREATE UNIQUE INDEX IF NOT EXISTS trades_idempotency_key_uq
			ON commerce.trades (idempotency_key) WHERE idempotency_key IS NOT NULL`,
		// Persisted rider → TigerBeetle wallet mapping (replaces hash-derived
		// account ids; sequential allocation starts at 1001).
		`CREATE TABLE IF NOT EXISTS commerce.rider_accounts (
			rider_sub  text PRIMARY KEY,
			account_id bigint NOT NULL UNIQUE,
			created_at timestamptz NOT NULL DEFAULT now()
		)`,
		// Loyalty tables: identical shape to migration 0005 (rider_sub key).
		// Migration 0005 renames user_sub → rider_sub on databases created by
		// migration 0003; the guarded rename below keeps pure-EnsureSchema
		// dev databases (service started before goose ran) consistent too.
		`DO $$
		BEGIN
			IF EXISTS (
				SELECT 1 FROM information_schema.columns
				WHERE table_schema = 'commerce' AND table_name = 'loyalty_accounts' AND column_name = 'user_sub'
			) AND NOT EXISTS (
				SELECT 1 FROM information_schema.columns
				WHERE table_schema = 'commerce' AND table_name = 'loyalty_accounts' AND column_name = 'rider_sub'
			) THEN
				ALTER TABLE commerce.loyalty_accounts RENAME COLUMN user_sub TO rider_sub;
			END IF;
		END $$`,
		`CREATE TABLE IF NOT EXISTS commerce.loyalty_accounts (
			rider_sub  text PRIMARY KEY,
			points     integer NOT NULL DEFAULT 0 CHECK (points >= 0),
			updated_at timestamptz NOT NULL DEFAULT now()
		)`,
		`CREATE TABLE IF NOT EXISTS commerce.loyalty_ledger (
			id         uuid PRIMARY KEY,
			rider_sub  text NOT NULL,
			delta      integer NOT NULL,
			reason     text NOT NULL,
			ref_id     text NOT NULL UNIQUE,
			created_at timestamptz NOT NULL DEFAULT now()
		)`,
		`CREATE TABLE IF NOT EXISTS commerce.loyalty_redemptions (
			id              uuid PRIMARY KEY,
			rider_sub       text NOT NULL,
			offer_id        uuid NOT NULL,
			points_spent    integer NOT NULL,
			idempotency_key text NOT NULL UNIQUE,
			status          text NOT NULL DEFAULT 'completed',
			created_at      timestamptz NOT NULL DEFAULT now()
		)`,
		`CREATE TABLE IF NOT EXISTS commerce.marketplace_offers (
			id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
			title       text NOT NULL,
			description text NOT NULL DEFAULT '',
			partner     text NOT NULL DEFAULT '',
			cost_points integer NOT NULL CHECK (cost_points > 0),
			active      boolean NOT NULL DEFAULT true,
			created_at  timestamptz NOT NULL DEFAULT now()
		)`,
		`CREATE TABLE IF NOT EXISTS commerce.ad_campaigns (
			id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
			name         text NOT NULL,
			advertiser   text NOT NULL DEFAULT '',
			budget_minor bigint NOT NULL DEFAULT 0,
			status       text NOT NULL DEFAULT 'draft',
			starts_at    timestamptz,
			ends_at      timestamptz,
			created_at   timestamptz NOT NULL DEFAULT now()
		)`,
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
	writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
