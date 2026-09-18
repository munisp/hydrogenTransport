package handlers

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
)

// Evidence pack (Wave-6 A4-04): a tamper-evident, chain-of-custody bundle
// for one incident, built for insurers, prosecutors and regulators.
//
// Contents:
//   - the incident row itself;
//   - the audit-log slice for the incident (platform.audit_log rows whose
//     entity_id is the incident), each with its hash-chain values;
//   - the telemetry window (±15 min around opened_at) for the involved bus;
//   - the webhook delivery record for the incident (who was told, when,
//     did they acknowledge receipt);
//   - a slice-linkage verification: every row's prev_hash must equal the
//     preceding row's hash, and the first row's prev_hash must equal the
//     hash of the audit entry immediately before the slice. That proves the
//     slice was not re-ordered, truncated or spliced — full-chain
//     recomputation remains available via audit-log GET /v1/audit/verify
//     (algorithm: docs/INSIDER_THREAT.md §2.2).

// AuditSliceRow is one platform.audit_log row in the evidence pack.
type AuditSliceRow struct {
	ID         int64           `json:"id"`
	ActorSub   string          `json:"actor_sub"`
	ActorRoles json.RawMessage `json:"actor_roles"`
	Action     string          `json:"action"`
	Entity     string          `json:"entity"`
	EntityID   string          `json:"entity_id"`
	TS         time.Time       `json:"ts"`
	PrevHash   string          `json:"prev_hash"`
	Hash       string          `json:"hash"`
}

// GetEvidencePack handles GET /v1/incidents/{id}/evidence-pack
// (platform-admin, auditor).
func (h *Handler) GetEvidencePack(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	incident, err := scanIncident(h.db.QueryRow(r.Context(),
		`SELECT `+incidentCols+` FROM infra.incidents WHERE id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "incident not found"})
		return
	}
	if err != nil {
		h.internal(w, "load incident for evidence pack", err)
		return
	}

	pack := map[string]any{
		"incident_id":   id,
		"incident":      incident,
		"generated_at":  time.Now().UTC().Format(time.RFC3339),
		"chain_custody": "audit rows carry their hash-chain values; slice_linkage verifies the slice is contiguous and unspliced; full-chain proof: GET /v1/audit/verify (audit-log)",
	}

	// Audit slice: every audit entry naming this incident (entity-scoped).
	rows, err := h.db.Query(r.Context(), `
		SELECT id, actor_sub, actor_roles, action, entity, entity_id, ts, prev_hash, hash
		FROM platform.audit_log
		WHERE entity_id = $1
		ORDER BY id`, id)
	if err != nil {
		h.internal(w, "load audit slice", err)
		return
	}
	slice := []AuditSliceRow{}
	for rows.Next() {
		var a AuditSliceRow
		if err := rows.Scan(&a.ID, &a.ActorSub, &a.ActorRoles, &a.Action, &a.Entity, &a.EntityID,
			&a.TS, &a.PrevHash, &a.Hash); err != nil {
			rows.Close()
			h.internal(w, "scan audit slice", err)
			return
		}
		slice = append(slice, a)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		h.internal(w, "iterate audit slice", err)
		return
	}
	pack["audit_slice"] = slice

	// Slice-linkage verification.
	linkage := map[string]any{"checked_rows": len(slice), "intact": true}
	for i := 1; i < len(slice); i++ {
		if slice[i].PrevHash != slice[i-1].Hash {
			linkage["intact"] = false
			linkage["break_at_id"] = slice[i].ID
			break
		}
	}
	if linkage["intact"] == true && len(slice) > 0 {
		var prevHash string
		err := h.db.QueryRow(r.Context(),
			`SELECT hash FROM platform.audit_log WHERE id < $1 ORDER BY id DESC LIMIT 1`,
			slice[0].ID).Scan(&prevHash)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			// Slice starts at the chain genesis — intact iff prev_hash is the
			// empty genesis value.
			if slice[0].PrevHash != "" {
				linkage["intact"] = false
				linkage["break_at_id"] = slice[0].ID
			}
		case err != nil:
			h.internal(w, "verify slice anchor", err)
			return
		default:
			if slice[0].PrevHash != prevHash {
				linkage["intact"] = false
				linkage["break_at_id"] = slice[0].ID
			}
		}
	}
	pack["slice_linkage"] = linkage

	// Telemetry window for the involved bus (±15 min around opened_at).
	if incident.BusID != nil {
		telemetry := []map[string]any{}
		trows, err := h.db.Query(r.Context(), `
			SELECT ts, speed_kph, h2_level_pct, fuel_cell_kw, battery_soc_pct,
			       ST_Y(geom)::float8, ST_X(geom)::float8
			FROM fleet.telemetry
			WHERE bus_id = $1 AND ts BETWEEN $2 - interval '15 minutes' AND $2 + interval '15 minutes'
			ORDER BY ts LIMIT 2000`, *incident.BusID, incident.OpenedAt)
		if err != nil {
			h.internal(w, "load telemetry window", err)
			return
		}
		for trows.Next() {
			var (
				ts                 time.Time
				speed, h2, fc, soc float64
				lat, lon           float64
			)
			if err := trows.Scan(&ts, &speed, &h2, &fc, &soc, &lat, &lon); err != nil {
				trows.Close()
				h.internal(w, "scan telemetry window", err)
				return
			}
			telemetry = append(telemetry, map[string]any{
				"ts": ts.UTC().Format(time.RFC3339), "speed_kph": speed,
				"h2_level_pct": h2, "fuel_cell_kw": fc, "battery_soc_pct": soc,
				"lat": lat, "lon": lon,
			})
		}
		trows.Close()
		if err := trows.Err(); err != nil {
			h.internal(w, "iterate telemetry window", err)
			return
		}
		pack["telemetry_window"] = telemetry
	}

	// Webhook delivery record: who was notified and did it land.
	deliveries := []map[string]any{}
	drows, err := h.db.Query(r.Context(), `
		SELECT d.id, s.url, d.event_type, d.status, d.attempts, d.created_at, d.delivered_at, d.last_error
		FROM infra.webhook_deliveries d
		JOIN infra.webhook_subscriptions s ON s.id = d.subscription_id
		WHERE d.incident_id = $1 ORDER BY d.created_at`, id)
	if err != nil {
		h.internal(w, "load webhook deliveries", err)
		return
	}
	for drows.Next() {
		var (
			did, url, eventType, status string
			attempts                    int
			createdAt                   time.Time
			deliveredAt                 *time.Time
			lastError                   *string
		)
		if err := drows.Scan(&did, &url, &eventType, &status, &attempts, &createdAt, &deliveredAt, &lastError); err != nil {
			drows.Close()
			h.internal(w, "scan webhook delivery", err)
			return
		}
		deliveries = append(deliveries, map[string]any{
			"id": did, "url": url, "event_type": eventType, "status": status,
			"attempts": attempts, "created_at": createdAt.UTC().Format(time.RFC3339),
			"delivered_at": deliveredAt, "last_error": lastError,
		})
	}
	drows.Close()
	if err := drows.Err(); err != nil {
		h.internal(w, "iterate webhook deliveries", err)
		return
	}
	pack["webhook_deliveries"] = deliveries

	writeJSON(w, http.StatusOK, pack)
}
