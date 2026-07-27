package handlers

import (
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"go.uber.org/zap"
)

// Station queue management (refueling-stations module, SPEC §1 "queue mgmt";
// BUSINESS_LOGIC_AUDIT §6). One active (waiting|serving) entry per bus per
// station, enforced by infra.station_queue's partial unique index (0005).
// Completing a serving entry with a dispensed kg amount decrements
// infra.stations.available_kg — the only automatic draw-down of station
// inventory besides energy-trade surplus sales.

// QueueEntry mirrors infra.station_queue plus the caller's queue position.
// Wave 5 (0008): the completion draw-down is energy-generic — DispensedUnit
// names the unit (kg for h2/cng, kwh for ev_charger, liters for diesel) and
// AvailableAfter is the station inventory left in that unit; the legacy
// dispensed_kg/available_after_kg fields are still set for kg-unit stations.
type QueueEntry struct {
	ID               string    `json:"id"`
	StationID        string    `json:"station_id"`
	BusID            string    `json:"bus_id"`
	JoinedAt         time.Time `json:"joined_at"`
	Status           string    `json:"status"`
	Position         int       `json:"position,omitempty"`
	EstWaitMinutes   int       `json:"est_wait_minutes,omitempty"`
	DispensedKg      *float64  `json:"dispensed_kg,omitempty"`
	AvailableAfterKg *float64  `json:"available_after_kg,omitempty"`
	DispensedAmount  *float64  `json:"dispensed_amount,omitempty"`
	DispensedUnit    string    `json:"dispensed_unit,omitempty"`
	AvailableAfter   *float64  `json:"available_after,omitempty"`
}

// inventoryColumn resolves the station inventory column + unit a draw-down
// applies to, branched by infra.stations.station_type (0008): h2/cng/mixed
// draw available_kg in kg, diesel draws available_kg in liters, ev_charger
// draws available_kwh in kwh. One numeric draw-down path; only the column
// and the unit name differ.
func inventoryColumn(stationType string) (column, unit string) {
	switch stationType {
	case "ev_charger":
		return "available_kwh", "kwh"
	case "diesel":
		return "available_kg", "liters"
	case "cng":
		return "available_kg", "kg"
	default: // h2, mixed, unknown → legacy H2 path
		return "available_kg", "kg"
	}
}

const queueEntryCols = `id, station_id, bus_id, joined_at, status`

func scanQueueEntry(row pgx.Row) (QueueEntry, error) {
	var e QueueEntry
	err := row.Scan(&e.ID, &e.StationID, &e.BusID, &e.JoinedAt, &e.Status)
	return e, err
}

// avgServiceMinutes estimates the per-bus service time at a station from
// completed queue history (joined→completed), defaulting to 15 minutes when
// no history exists.
func (h *Handler) avgServiceMinutes(r *http.Request, stationID string) float64 {
	var mins *float64
	if err := h.db.QueryRow(r.Context(), `
		SELECT avg(EXTRACT(EPOCH FROM (completed_at - joined_at)) / 60)::float8
		FROM infra.station_queue
		WHERE station_id = $1 AND status = 'completed' AND completed_at IS NOT NULL`,
		stationID).Scan(&mins); err != nil || mins == nil || *mins <= 0 {
		return 15.0
	}
	return *mins
}

// JoinStationQueue handles POST /v1/stations/{id}/queue {bus_id} (Keycloak
// JWT). The bus joins as 'waiting' (or 'serving' immediately when the queue
// is empty). 409 when the bus already holds an active entry at this station,
// 404 for an unknown station, 422 for an unknown bus.
func (h *Handler) JoinStationQueue(w http.ResponseWriter, r *http.Request) {
	stationID := chi.URLParam(r, "id")
	var req struct {
		BusID string `json:"bus_id"`
	}
	if err := decodeJSON(w, r, &req); err != nil || req.BusID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "body must include \"bus_id\""})
		return
	}

	tx, err := h.db.Begin(r.Context())
	if err != nil {
		h.internal(w, "begin queue join", err)
		return
	}
	defer func() { _ = tx.Rollback(r.Context()) }()

	// Station must exist and be able to serve (online).
	var status string
	if err := tx.QueryRow(r.Context(),
		`SELECT COALESCE(status,'unknown') FROM infra.stations WHERE id = $1`, stationID).Scan(&status); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "station not found"})
		} else {
			h.internal(w, "load station for queue join", err)
		}
		return
	}
	if status != "online" {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "station is not online (status " + status + ")"})
		return
	}

	// Serve immediately when nobody is ahead; otherwise join the tail.
	var active int
	if err := tx.QueryRow(r.Context(), `
		SELECT count(*) FROM infra.station_queue
		WHERE station_id = $1 AND status IN ('waiting','serving')`, stationID).Scan(&active); err != nil {
		h.internal(w, "count queue", err)
		return
	}
	entryStatus := "waiting"
	if active == 0 {
		entryStatus = "serving"
	}

	e, err := scanQueueEntry(tx.QueryRow(r.Context(), `
		INSERT INTO infra.station_queue (station_id, bus_id, status)
		VALUES ($1, $2, $3)
		RETURNING `+queueEntryCols, stationID, req.BusID, entryStatus))
	if err != nil {
		var pgErr *pgconn.PgError
		switch {
		case errors.As(err, &pgErr) && pgErr.Code == "23505":
			writeJSON(w, http.StatusConflict, map[string]string{"error": "bus already has an active queue entry at this station"})
		case errors.As(err, &pgErr) && pgErr.Code == "23503":
			writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": "bus_id does not reference a known vehicle"})
		default:
			h.internal(w, "join station queue", err)
		}
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		h.internal(w, "commit queue join", err)
		return
	}

	if e.Status == "waiting" {
		e.Position = active + 1
		e.EstWaitMinutes = int(float64(active) * h.avgServiceMinutes(r, stationID))
	}
	writeJSON(w, http.StatusCreated, e)
}

// ListStationQueue handles GET /v1/stations/{id}/queue — active entries in
// join order with position and estimated wait derived from the station's
// historical service time.
func (h *Handler) ListStationQueue(w http.ResponseWriter, r *http.Request) {
	stationID := chi.URLParam(r, "id")
	rows, err := h.db.Query(r.Context(), `
		SELECT `+queueEntryCols+` FROM infra.station_queue
		WHERE station_id = $1 AND status IN ('waiting','serving')
		ORDER BY joined_at`, stationID)
	if err != nil {
		h.internal(w, "list station queue", err)
		return
	}
	defer rows.Close()

	entries := []QueueEntry{}
	for rows.Next() {
		e, err := scanQueueEntry(rows)
		if err != nil {
			h.internal(w, "scan queue entry", err)
			return
		}
		entries = append(entries, e)
	}
	if err := rows.Err(); err != nil {
		h.internal(w, "iterate station queue", err)
		return
	}

	serviceMin := h.avgServiceMinutes(r, stationID)
	position := 0
	for i := range entries {
		if entries[i].Status == "serving" {
			continue
		}
		position++
		entries[i].Position = position
		entries[i].EstWaitMinutes = int(float64(position-1) * serviceMin)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"station_id":          stationID,
		"queue":               entries,
		"avg_service_minutes": serviceMin,
	})
}

// advanceQueue promotes the longest-waiting entry to 'serving' after the
// current serving entry leaves the dispenser.
func (h *Handler) advanceQueue(r *http.Request, stationID string) {
	tag, err := h.db.Exec(r.Context(), `
		UPDATE infra.station_queue SET status = 'serving'
		WHERE id = (
			SELECT id FROM infra.station_queue
			WHERE station_id = $1 AND status = 'waiting'
			ORDER BY joined_at LIMIT 1
		)`, stationID)
	if err != nil {
		h.log.Error("advance station queue failed", zap.String("station", stationID), zap.Error(err))
	} else if tag.RowsAffected() > 0 {
		h.log.Info("station queue advanced", zap.String("station", stationID))
	}
}

// CompleteStationQueueEntry handles POST
// /v1/stations/{id}/queue/{entry}/complete {dispensed_kg|dispensed_amount}
// (Keycloak JWT, operator): marks the serving entry completed and draws down
// the station inventory by the dispensed amount (409 when the recorded
// inventory cannot cover it). Wave 5 (0008): the draw-down branches by
// station_type — h2/cng/mixed decrement available_kg (unit kg), diesel
// decrements available_kg (unit liters), ev_charger decrements
// available_kwh (unit kwh); dispensed_amount is the energy-generic alias of
// dispensed_kg. The next waiting bus is promoted to serving.
func (h *Handler) CompleteStationQueueEntry(w http.ResponseWriter, r *http.Request) {
	stationID, entryID := chi.URLParam(r, "id"), chi.URLParam(r, "entry")
	var req struct {
		DispensedKg     *float64 `json:"dispensed_kg"`
		DispensedAmount *float64 `json:"dispensed_amount"`
	}
	if err := decodeJSON(w, r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
		return
	}
	amount := req.DispensedAmount
	if amount == nil {
		amount = req.DispensedKg
	}
	if amount != nil && *amount < 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "dispensed amount must not be negative"})
		return
	}

	tx, err := h.db.Begin(r.Context())
	if err != nil {
		h.internal(w, "begin queue complete", err)
		return
	}
	defer func() { _ = tx.Rollback(r.Context()) }()

	var e QueueEntry
	var available float64
	var stationType string
	err = tx.QueryRow(r.Context(), `
		WITH done AS (
			UPDATE infra.station_queue SET status = 'completed', completed_at = now()
			WHERE id = $1 AND station_id = $2 AND status = 'serving'
			RETURNING `+queueEntryCols+`
		)
		SELECT done.*, (
			SELECT COALESCE(available_kg,0) FROM infra.stations WHERE id = $2
		), (
			SELECT COALESCE(station_type,'h2') FROM infra.stations WHERE id = $2
		) FROM done`,
		entryID, stationID).
		Scan(&e.ID, &e.StationID, &e.BusID, &e.JoinedAt, &e.Status, &available, &stationType)
	if errors.Is(err, pgx.ErrNoRows) {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "queue entry not found or not in serving status"})
		return
	}
	if err != nil {
		h.internal(w, "complete queue entry", err)
		return
	}

	if amount != nil && *amount > 0 {
		column, unit := inventoryColumn(stationType)
		if column == "available_kwh" {
			// EV stations track deliverable kWh, not kg.
			var availKwh float64
			if err := tx.QueryRow(r.Context(),
				`SELECT COALESCE(available_kwh,0) FROM infra.stations WHERE id = $1`, stationID).
				Scan(&availKwh); err != nil {
				h.internal(w, "load station kwh inventory", err)
				return
			}
			available = availKwh
		}
		if *amount > available+1e-9 {
			writeJSON(w, http.StatusConflict, map[string]any{
				"error":           "insufficient_inventory",
				"message":         "dispensed amount exceeds the station's recorded " + column,
				"available":       available,
				"available_field": column,
				"dispensed":       *amount,
				"unit":            unit,
			})
			return
		}
		if err := tx.QueryRow(r.Context(), `
			UPDATE infra.stations SET `+column+` = `+column+` - $2
			WHERE id = $1 RETURNING COALESCE(`+column+`,0)`, stationID, *amount).
			Scan(&available); err != nil {
			h.internal(w, "decrement station inventory", err)
			return
		}
		e.DispensedAmount = amount
		e.DispensedUnit = unit
		e.AvailableAfter = &available
		if column == "available_kg" {
			// Legacy kg fields (h2 backward compat; also kg/liters for
			// cng/diesel which share the available_kg column).
			e.DispensedKg = amount
			e.AvailableAfterKg = &available
		}
	}
	if err := tx.Commit(r.Context()); err != nil {
		h.internal(w, "commit queue complete", err)
		return
	}

	h.advanceQueue(r, stationID)
	writeJSON(w, http.StatusOK, e)
}

// LeaveStationQueue handles POST /v1/stations/{id}/queue/{entry}/leave
// (Keycloak JWT): the bus leaves the queue (waiting|serving → left). When the
// leaving entry was serving, the next waiting bus is promoted.
func (h *Handler) LeaveStationQueue(w http.ResponseWriter, r *http.Request) {
	stationID, entryID := chi.URLParam(r, "id"), chi.URLParam(r, "entry")
	e, err := scanQueueEntry(h.db.QueryRow(r.Context(), `
		UPDATE infra.station_queue SET status = 'left'
		WHERE id = $1 AND station_id = $2 AND status IN ('waiting','serving')
		RETURNING `+queueEntryCols, entryID, stationID))
	if errors.Is(err, pgx.ErrNoRows) {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "queue entry not found or already inactive"})
		return
	}
	if err != nil {
		h.internal(w, "leave station queue", err)
		return
	}
	h.advanceQueue(r, stationID)
	writeJSON(w, http.StatusOK, e)
}
