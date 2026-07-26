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
// /v1/stations/{id}/queue/{entry}/complete {dispensed_kg} (Keycloak JWT,
// operator): marks the serving entry completed and decrements
// infra.stations.available_kg by the dispensed amount (409 when the recorded
// inventory cannot cover it). The next waiting bus is promoted to serving.
func (h *Handler) CompleteStationQueueEntry(w http.ResponseWriter, r *http.Request) {
	stationID, entryID := chi.URLParam(r, "id"), chi.URLParam(r, "entry")
	var req struct {
		DispensedKg *float64 `json:"dispensed_kg"`
	}
	if err := decodeJSON(w, r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
		return
	}
	if req.DispensedKg != nil && *req.DispensedKg < 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "dispensed_kg must not be negative"})
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
	err = tx.QueryRow(r.Context(), `
		WITH done AS (
			UPDATE infra.station_queue SET status = 'completed', completed_at = now()
			WHERE id = $1 AND station_id = $2 AND status = 'serving'
			RETURNING `+queueEntryCols+`
		)
		SELECT done.*, (SELECT COALESCE(available_kg,0) FROM infra.stations WHERE id = $2) FROM done`,
		entryID, stationID).
		Scan(&e.ID, &e.StationID, &e.BusID, &e.JoinedAt, &e.Status, &available)
	if errors.Is(err, pgx.ErrNoRows) {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "queue entry not found or not in serving status"})
		return
	}
	if err != nil {
		h.internal(w, "complete queue entry", err)
		return
	}

	if req.DispensedKg != nil && *req.DispensedKg > 0 {
		if *req.DispensedKg > available+1e-9 {
			writeJSON(w, http.StatusConflict, map[string]any{
				"error":        "insufficient_inventory",
				"message":      "dispensed_kg exceeds the station's recorded available_kg",
				"available_kg": available,
				"dispensed_kg": *req.DispensedKg,
			})
			return
		}
		if err := tx.QueryRow(r.Context(), `
			UPDATE infra.stations SET available_kg = available_kg - $2
			WHERE id = $1 RETURNING COALESCE(available_kg,0)`, stationID, *req.DispensedKg).
			Scan(&available); err != nil {
			h.internal(w, "decrement station inventory", err)
			return
		}
		e.DispensedKg = req.DispensedKg
		e.AvailableAfterKg = &available
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
