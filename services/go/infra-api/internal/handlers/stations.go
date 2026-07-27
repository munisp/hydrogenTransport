package handlers

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"go.uber.org/zap"
)

// Station mirrors infra.stations (geom exposed as lat/lon). Wave 5 (0008):
// station_type names the energy domain; available_kwh/charger_count are the
// EV inventory fields (NULL for pure H2 stations).
type Station struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	CapacityKg   float64  `json:"capacity_kg"`
	AvailableKg  float64  `json:"available_kg"`
	Status       string   `json:"status"`
	StationType  string   `json:"station_type"` // h2|ev_charger|diesel|cng|mixed
	AvailableKwh *float64 `json:"available_kwh,omitempty"`
	ChargerCount *int     `json:"charger_count,omitempty"`
	Lat          *float64 `json:"lat,omitempty"`
	Lon          *float64 `json:"lon,omitempty"`
}

// stationTypes is the CHECK constraint domain of infra.stations.station_type.
var stationTypes = map[string]bool{"h2": true, "ev_charger": true, "diesel": true, "cng": true, "mixed": true}

const stationCols = `id, name, COALESCE(capacity_kg,0), COALESCE(available_kg,0),
	COALESCE(status,'unknown'), COALESCE(station_type,'h2'), available_kwh, charger_count,
	ST_Y(geom)::float8, ST_X(geom)::float8`

func scanStation(row pgx.Row) (Station, error) {
	var s Station
	err := row.Scan(&s.ID, &s.Name, &s.CapacityKg, &s.AvailableKg, &s.Status,
		&s.StationType, &s.AvailableKwh, &s.ChargerCount, &s.Lat, &s.Lon)
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
	Name         string   `json:"name"`
	CapacityKg   float64  `json:"capacity_kg"`
	AvailableKg  float64  `json:"available_kg"`
	Status       string   `json:"status"`
	StationType  string   `json:"station_type"`
	AvailableKwh *float64 `json:"available_kwh"`
	ChargerCount *int     `json:"charger_count"`
	Lat          *float64 `json:"lat"`
	Lon          *float64 `json:"lon"`
}

// CreateStation handles POST /v1/stations (Keycloak JWT required).
func (h *Handler) CreateStation(w http.ResponseWriter, r *http.Request) {
	var req createStationRequest
	if err := decodeJSON(w, r, &req); err != nil {
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
	if req.StationType == "" {
		req.StationType = "h2"
	}
	if !stationTypes[req.StationType] {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "station_type must be one of h2|ev_charger|diesel|cng|mixed"})
		return
	}

	var geomArg any
	if req.Lat != nil && req.Lon != nil {
		geomArg = pgxPoint{Lon: *req.Lon, Lat: *req.Lat}
	}

	s, err := scanStation(h.db.QueryRow(r.Context(), `
		INSERT INTO infra.stations (name, capacity_kg, available_kg, status, station_type, available_kwh, charger_count, geom)
		VALUES ($1, $2, $3, $4, $5, $6, $7,
			CASE WHEN $8::jsonb IS NULL THEN NULL
			     ELSE ST_SetSRID(ST_MakePoint(
				($8::jsonb->>'lon')::float8, ($8::jsonb->>'lat')::float8), 4326) END)
		RETURNING `+stationCols,
		req.Name, req.CapacityKg, req.AvailableKg, req.Status, req.StationType,
		req.AvailableKwh, req.ChargerCount, nullableJSON(geomArg)))
	if err != nil {
		h.internal(w, "create station", err)
		return
	}
	writeJSON(w, http.StatusCreated, s)
}

type stationStatusRequest struct {
	Status       string   `json:"status"`
	AvailableKg  *float64 `json:"available_kg"`
	AvailableKwh *float64 `json:"available_kwh"` // Wave 5: EV inventory (ev_charger stations)
}

// UpdateStationStatus handles PATCH /v1/stations/{id}/status (Keycloak JWT).
// Publishes station.status.changed (SPEC §3.3; Wave 5 adds station_type and
// available_kwh to the event — additive, H2 payload unchanged).
func (h *Handler) UpdateStationStatus(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var req stationStatusRequest
	if err := decodeJSON(w, r, &req); err != nil || req.Status == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "body must include \"status\""})
		return
	}

	s, err := scanStation(h.db.QueryRow(r.Context(), `
		UPDATE infra.stations SET
			status        = $2,
			available_kg  = CASE WHEN $3::float8 IS NULL THEN available_kg  ELSE $3::float8 END,
			available_kwh = CASE WHEN $4::float8 IS NULL THEN available_kwh ELSE $4::float8 END
		WHERE id = $1
		RETURNING `+stationCols, id, req.Status, req.AvailableKg, req.AvailableKwh))
	if errors.Is(err, pgx.ErrNoRows) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "station not found"})
		return
	}
	if err != nil {
		h.internal(w, "update station status", err)
		return
	}

	event := map[string]any{
		"station_id":   s.ID,
		"status":       s.Status,
		"available_kg": s.AvailableKg,
		"station_type": s.StationType,
	}
	if s.AvailableKwh != nil {
		event["available_kwh"] = *s.AvailableKwh
	}
	if err := h.pub.Publish(r.Context(), "station.status.changed", event); err != nil {
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
