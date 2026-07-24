package handlers

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"go.uber.org/zap"
)

// Station mirrors infra.stations (geom exposed as lat/lon).
type Station struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	CapacityKg  float64  `json:"capacity_kg"`
	AvailableKg float64  `json:"available_kg"`
	Status      string   `json:"status"`
	Lat         *float64 `json:"lat,omitempty"`
	Lon         *float64 `json:"lon,omitempty"`
}

const stationCols = `id, name, COALESCE(capacity_kg,0), COALESCE(available_kg,0),
	COALESCE(status,'unknown'), ST_Y(geom)::float8, ST_X(geom)::float8`

func scanStation(row pgx.Row) (Station, error) {
	var s Station
	err := row.Scan(&s.ID, &s.Name, &s.CapacityKg, &s.AvailableKg, &s.Status, &s.Lat, &s.Lon)
	return s, err
}

// ListStations handles GET /v1/stations (refueling-stations module).
func (h *Handler) ListStations(w http.ResponseWriter, r *http.Request) {
	rows, err := h.db.Query(r.Context(),
		`SELECT `+stationCols+` FROM infra.stations ORDER BY name LIMIT 200`)
	if err != nil {
		h.internal(w, "list stations", err)
		return
	}
	defer rows.Close()

	stations := []Station{}
	for rows.Next() {
		s, err := scanStation(rows)
		if err != nil {
			h.internal(w, "scan station", err)
			return
		}
		stations = append(stations, s)
	}
	if err := rows.Err(); err != nil {
		h.internal(w, "iterate stations", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"stations": stations})
}

// GetStation handles GET /v1/stations/{id}.
func (h *Handler) GetStation(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	s, err := scanStation(h.db.QueryRow(r.Context(),
		`SELECT `+stationCols+` FROM infra.stations WHERE id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "station not found"})
		return
	}
	if err != nil {
		h.internal(w, "get station", err)
		return
	}
	writeJSON(w, http.StatusOK, s)
}

type createStationRequest struct {
	Name        string   `json:"name"`
	CapacityKg  float64  `json:"capacity_kg"`
	AvailableKg float64  `json:"available_kg"`
	Status      string   `json:"status"`
	Lat         *float64 `json:"lat"`
	Lon         *float64 `json:"lon"`
}

// CreateStation handles POST /v1/stations (Keycloak JWT required).
func (h *Handler) CreateStation(w http.ResponseWriter, r *http.Request) {
	var req createStationRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
		return
	}
	if req.Name == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "name is required"})
		return
	}
	if req.Status == "" {
		req.Status = "online"
	}

	var geomArg any
	if req.Lat != nil && req.Lon != nil {
		geomArg = pgxPoint{Lon: *req.Lon, Lat: *req.Lat}
	}

	var s Station
	err := h.db.QueryRow(r.Context(), `
		INSERT INTO infra.stations (name, capacity_kg, available_kg, status, geom)
		VALUES ($1, $2, $3, $4,
			CASE WHEN $5::jsonb IS NULL THEN NULL
			     ELSE ST_SetSRID(ST_MakePoint(
				($5::jsonb->>'lon')::float8, ($5::jsonb->>'lat')::float8), 4326) END)
		RETURNING `+stationCols,
		req.Name, req.CapacityKg, req.AvailableKg, req.Status, nullableJSON(geomArg)).Scan(
		&s.ID, &s.Name, &s.CapacityKg, &s.AvailableKg, &s.Status, &s.Lat, &s.Lon)
	if err != nil {
		h.internal(w, "create station", err)
		return
	}
	writeJSON(w, http.StatusCreated, s)
}

type stationStatusRequest struct {
	Status      string   `json:"status"`
	AvailableKg *float64 `json:"available_kg"`
}

// UpdateStationStatus handles PATCH /v1/stations/{id}/status (Keycloak JWT).
// Publishes station.status.changed (SPEC §3.3).
func (h *Handler) UpdateStationStatus(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var req stationStatusRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Status == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "body must include \"status\""})
		return
	}

	var s Station
	var err error
	if req.AvailableKg != nil {
		err = h.db.QueryRow(r.Context(), `
			UPDATE infra.stations SET status = $2, available_kg = $3 WHERE id = $1
			RETURNING `+stationCols, id, req.Status, *req.AvailableKg).Scan(
			&s.ID, &s.Name, &s.CapacityKg, &s.AvailableKg, &s.Status, &s.Lat, &s.Lon)
	} else {
		err = h.db.QueryRow(r.Context(), `
			UPDATE infra.stations SET status = $2 WHERE id = $1
			RETURNING `+stationCols, id, req.Status).Scan(
			&s.ID, &s.Name, &s.CapacityKg, &s.AvailableKg, &s.Status, &s.Lat, &s.Lon)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "station not found"})
		return
	}
	if err != nil {
		h.internal(w, "update station status", err)
		return
	}

	if err := h.pub.Publish(r.Context(), "station.status.changed", map[string]any{
		"station_id":   s.ID,
		"status":       s.Status,
		"available_kg": s.AvailableKg,
	}); err != nil {
		h.log.Error("failed to publish station.status.changed", zap.Error(err))
	}
	writeJSON(w, http.StatusOK, s)
}

// pgxPoint is marshalled to JSON and decoded server-side into a PostGIS point.
type pgxPoint struct {
	Lon float64 `json:"lon"`
	Lat float64 `json:"lat"`
}

func nullableJSON(v any) any {
	if v == nil {
		return nil
	}
	b, _ := json.Marshal(v)
	return string(b)
}
