// Package handlers serves the audit-log HTTP API (see openapi.yaml):
//
//	POST /v1/audit         service-to-service ingest (X-Audit-Token or JWT)
//	GET  /v1/audit         platform-admin search (?actor=&entity=&from=&limit=)
//	GET  /v1/audit/verify  platform-admin hash-chain integrity check
//	GET  /healthz          liveness/readiness (pings Postgres)
package handlers

import (
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"go.uber.org/zap"

	"github.com/munisp/hydrogenTransport/services/go/audit-log/internal/anomaly"
	"github.com/munisp/hydrogenTransport/services/go/audit-log/internal/mirror"
	"github.com/munisp/hydrogenTransport/services/go/audit-log/internal/store"
)

var entriesTotal = promauto.NewCounter(prometheus.CounterOpts{
	Name: "audit_entries_total",
	Help: "Audit entries appended to the hash-chained trail.",
})

// Handler wires the endpoints to their collaborators.
type Handler struct {
	store    store.Store
	mirror   *mirror.Mirror
	detector *anomaly.Detector
	log      *zap.Logger
}

// New builds a Handler.
func New(st store.Store, m *mirror.Mirror, det *anomaly.Detector, log *zap.Logger) *Handler {
	return &Handler{store: st, mirror: m, detector: det, log: log}
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func (h *Handler) internal(w http.ResponseWriter, op string, err error) {
	h.log.Error(op, zap.Error(err))
	writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
}

// Healthz reports liveness/readiness including Postgres.
func (h *Handler) Healthz(w http.ResponseWriter, r *http.Request) {
	if err := h.store.Ping(r.Context()); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "unhealthy"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

type ingestRequest struct {
	ActorSub   string           `json:"actor_sub"`
	ActorRoles []string         `json:"actor_roles"`
	Action     string           `json:"action"`
	Entity     string           `json:"entity"`
	EntityID   string           `json:"entity_id"`
	Before     *json.RawMessage `json:"before"`
	After      *json.RawMessage `json:"after"`
	IP         string           `json:"ip"`
	UA         string           `json:"ua"`
	TS         *time.Time       `json:"ts"`
}

// Ingest handles POST /v1/audit. Auth is enforced by middleware (shared
// token or JWT) before this handler runs.
func (h *Handler) Ingest(w http.ResponseWriter, r *http.Request) {
	var req ingestRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
		return
	}
	if req.ActorSub == "" || req.Action == "" || req.Entity == "" {
		writeJSON(w, http.StatusBadRequest,
			map[string]string{"error": "actor_sub, action and entity are required"})
		return
	}
	e := store.Entry{
		ActorSub:   req.ActorSub,
		ActorRoles: req.ActorRoles,
		Action:     req.Action,
		Entity:     req.Entity,
		EntityID:   req.EntityID,
		Before:     req.Before,
		After:      req.After,
		IP:         req.IP,
		UA:         req.UA,
	}
	if req.TS != nil {
		e.TS = req.TS.UTC()
	}
	if err := h.store.Append(r.Context(), &e); err != nil {
		h.internal(w, "append audit entry", err)
		return
	}
	entriesTotal.Inc()
	// Anomaly detection is in-band (cheap map update) — it must never block
	// or fail the audit write.
	h.detector.Observe(e.ActorSub, e.Action, e.Entity)
	// OpenSearch mirror is best-effort and async.
	if h.mirror.Enabled() {
		go h.mirror.Publish(e)
	}
	writeJSON(w, http.StatusCreated, e)
}

// List handles GET /v1/audit (platform-admin only; enforced by middleware).
func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := store.ListFilter{Actor: q.Get("actor"), Entity: q.Get("entity")}
	if v := q.Get("from"); v != "" {
		ts, err := time.Parse(time.RFC3339, v)
		if err != nil {
			writeJSON(w, http.StatusBadRequest,
				map[string]string{"error": "from must be RFC3339 (e.g. 2025-01-02T03:04:05Z)"})
			return
		}
		f.From = ts
	}
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "limit must be an integer"})
			return
		}
		f.Limit = n
	}
	entries, err := h.store.List(r.Context(), f)
	if err != nil {
		h.internal(w, "list audit entries", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"entries": entries, "count": len(entries)})
}

// Verify handles GET /v1/audit/verify (platform-admin only).
func (h *Handler) Verify(w http.ResponseWriter, r *http.Request) {
	badID, checked, err := h.store.Verify(r.Context())
	if err != nil {
		h.internal(w, "verify audit chain", err)
		return
	}
	ok := badID == 0
	code := http.StatusOK
	if !ok {
		code = http.StatusConflict // chain broken — tamper evidence triggered
	}
	writeJSON(w, code, map[string]any{
		"ok":           ok,
		"checked":      checked,
		"first_bad_id": badID,
		"checked_at":   time.Now().UTC().Format(time.RFC3339),
	})
}

// RequireIngestAuth allows a request carrying either the shared
// service-to-service token (X-Audit-Token, LEAK-ingest-style) or — when that
// does not match — falls through to the JWT middleware. When ingestToken is
// empty only JWT is accepted.
func RequireIngestAuth(ingestToken string, jwtMW func(http.Handler) http.Handler) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		tokenOK := func(r *http.Request) bool {
			if ingestToken == "" {
				return false
			}
			got := r.Header.Get("X-Audit-Token")
			return subtle.ConstantTimeCompare([]byte(got), []byte(ingestToken)) == 1
		}
		jwtWrapped := jwtMW(next)
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if tokenOK(r) {
				next.ServeHTTP(w, r)
				return
			}
			jwtWrapped.ServeHTTP(w, r)
		})
	}
}
