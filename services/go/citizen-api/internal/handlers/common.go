// Package handlers implements the Domain 3 (citizen & engagement) API.
package handlers

import (
	"encoding/json"
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/zap"

	toggle "github.com/munisp/hydrogenTransport/packages/toggle-client/go"
	"github.com/munisp/hydrogenTransport/services/go/citizen-api/internal/pubsub"
)

// Handler serves the citizen endpoints. db may be nil when DATABASE_URL is
// unset; DB-backed endpoints then respond 503.
type Handler struct {
	db  DB
	pub pubsub.Publisher
	tc  *toggle.Client
	log *zap.Logger
}

// New builds a Handler. A nil pool stays a nil DB (not a non-nil interface
// wrapping a nil pointer) so requireDB keeps reporting 503.
func New(db *pgxpool.Pool, pub pubsub.Publisher, tc *toggle.Client, log *zap.Logger) *Handler {
	var d DB
	if db != nil {
		d = db
	}
	return &Handler{db: d, pub: pub, tc: tc, log: log}
}

// Healthz reports liveness/readiness.
func (h *Handler) Healthz(w http.ResponseWriter, r *http.Request) {
	if h.db != nil {
		if err := h.db.Ping(r.Context()); err != nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "degraded", "postgres": err.Error()})
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// requireDB responds 503 when the database is not configured.
func (h *Handler) requireDB(w http.ResponseWriter) bool {
	if h.db == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "database not configured (DATABASE_URL unset)"})
		return false
	}
	return true
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
