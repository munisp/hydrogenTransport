// Package handlers implements the Domain 1 (fleet) API (SPEC §3.4 fleet schema).
package handlers

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/zap"
)

// Handler serves the fleet endpoints.
type Handler struct {
	db  *pgxpool.Pool
	log *zap.Logger
}

// New builds a Handler.
func New(db *pgxpool.Pool, log *zap.Logger) *Handler { return &Handler{db: db, log: log} }

// Healthz reports liveness/readiness.
func (h *Handler) Healthz(w http.ResponseWriter, r *http.Request) {
	if err := h.db.Ping(r.Context()); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "degraded", "postgres": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// Vehicle mirrors fleet.vehicles (geom exposed as lat/lon).
type Vehicle struct {
	ID           string   `json:"id"`
	FleetNo      string   `json:"fleet_no"`
	VIN          string   `json:"vin"`
	Model        string   `json:"model"`
	H2CapacityKg float64  `json:"h2_capacity_kg"`
	Status       string   `json:"status"`
	Lat          *float64 `json:"lat,omitempty"`
	Lon          *float64 `json:"lon,omitempty"`
}

const vehicleCols = `id, fleet_no, COALESCE(vin,''), COALESCE(model,''),
	COALESCE(h2_capacity_kg,0), COALESCE(status,'unknown'),
	ST_Y(geom)::float8, ST_X(geom)::float8`

func scanVehicle(row pgx.Row) (Vehicle, error) {
	var v Vehicle
	err := row.Scan(&v.ID, &v.FleetNo, &v.VIN, &v.Model, &v.H2CapacityKg, &v.Status, &v.Lat, &v.Lon)
	return v, err
}

// ListVehicles handles GET /v1/vehicles.
func (h *Handler) ListVehicles(w http.ResponseWriter, r *http.Request) {
	rows, err := h.db.Query(r.Context(),
		`SELECT `+vehicleCols+` FROM fleet.vehicles ORDER BY fleet_no LIMIT 500`)
	if err != nil {
		h.internal(w, "list vehicles", err)
		return
	}
	defer rows.Close()

	vehicles := []Vehicle{}
	for rows.Next() {
		v, err := scanVehicle(rows)
		if err != nil {
			h.internal(w, "scan vehicle", err)
			return
		}
		vehicles = append(vehicles, v)
	}
	if err := rows.Err(); err != nil {
		h.internal(w, "iterate vehicles", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"vehicles": vehicles})
}

// GetVehicle handles GET /v1/vehicles/{id}.
func (h *Handler) GetVehicle(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	v, err := scanVehicle(h.db.QueryRow(r.Context(),
		`SELECT `+vehicleCols+` FROM fleet.vehicles WHERE id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "vehicle not found"})
		return
	}
	if err != nil {
		h.internal(w, "get vehicle", err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

// TelemetryPoint mirrors fleet.telemetry (TimescaleDB hypertable).
type TelemetryPoint struct {
	TS            time.Time `json:"ts"`
	SpeedKph      float64   `json:"speed_kph"`
	H2LevelPct    float64   `json:"h2_level_pct"`
	FuelCellKw    float64   `json:"fuel_cell_kw"`
	BatterySocPct float64   `json:"battery_soc_pct"`
	OdometerKm    float64   `json:"odometer_km"`
	Lat           *float64  `json:"lat,omitempty"`
	Lon           *float64  `json:"lon,omitempty"`
}

// GetTelemetry handles GET /v1/vehicles/{id}/telemetry?from&to
// (RFC3339 timestamps; default window: last 24h).
func (h *Handler) GetTelemetry(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	to := parseTimeParam(r, "to", time.Now().UTC())
	from := parseTimeParam(r, "from", to.Add(-24*time.Hour))
	if from.After(to) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "from must be before to"})
		return
	}

	rows, err := h.db.Query(r.Context(), `
		SELECT ts, COALESCE(speed_kph,0), COALESCE(h2_level_pct,0), COALESCE(fuel_cell_kw,0),
		       COALESCE(battery_soc_pct,0), COALESCE(odometer_km,0),
		       ST_Y(geom)::float8, ST_X(geom)::float8
		FROM fleet.telemetry
		WHERE bus_id = $1 AND ts BETWEEN $2 AND $3
		ORDER BY ts
		LIMIT 5000`, id, from, to)
	if err != nil {
		h.internal(w, "query telemetry", err)
		return
	}
	defer rows.Close()

	points := []TelemetryPoint{}
	for rows.Next() {
		var p TelemetryPoint
		if err := rows.Scan(&p.TS, &p.SpeedKph, &p.H2LevelPct, &p.FuelCellKw,
			&p.BatterySocPct, &p.OdometerKm, &p.Lat, &p.Lon); err != nil {
			h.internal(w, "scan telemetry", err)
			return
		}
		points = append(points, p)
	}
	if err := rows.Err(); err != nil {
		h.internal(w, "iterate telemetry", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"vehicle_id": id, "from": from, "to": to, "points": points,
	})
}

// TelemetrySample is the latest telemetry point for one bus (telematics
// module) — same JSON shape as TelemetryPoint plus the bus identity.
type TelemetrySample struct {
	BusID         string    `json:"bus_id"`
	TS            time.Time `json:"ts"`
	SpeedKph      float64   `json:"speed_kph"`
	H2LevelPct    float64   `json:"h2_level_pct"`
	FuelCellKw    float64   `json:"fuel_cell_kw"`
	BatterySocPct float64   `json:"battery_soc_pct"`
	OdometerKm    float64   `json:"odometer_km"`
	Lat           *float64  `json:"lat,omitempty"`
	Lon           *float64  `json:"lon,omitempty"`
}

// LatestTelemetry handles GET /v1/telemetry/latest: the most recent telemetry
// sample per bus (DISTINCT ON bus_id, ordered by ts DESC).
func (h *Handler) LatestTelemetry(w http.ResponseWriter, r *http.Request) {
	rows, err := h.db.Query(r.Context(), `
		SELECT DISTINCT ON (bus_id)
		       bus_id, ts, COALESCE(speed_kph,0), COALESCE(h2_level_pct,0),
		       COALESCE(fuel_cell_kw,0), COALESCE(battery_soc_pct,0), COALESCE(odometer_km,0),
		       ST_Y(geom)::float8, ST_X(geom)::float8
		FROM fleet.telemetry
		ORDER BY bus_id, ts DESC`)
	if err != nil {
		h.internal(w, "query latest telemetry", err)
		return
	}
	defer rows.Close()

	samples := []TelemetrySample{}
	for rows.Next() {
		var s TelemetrySample
		if err := rows.Scan(&s.BusID, &s.TS, &s.SpeedKph, &s.H2LevelPct, &s.FuelCellKw,
			&s.BatterySocPct, &s.OdometerKm, &s.Lat, &s.Lon); err != nil {
			h.internal(w, "scan latest telemetry", err)
			return
		}
		samples = append(samples, s)
	}
	if err := rows.Err(); err != nil {
		h.internal(w, "iterate latest telemetry", err)
		return
	}
	writeJSON(w, http.StatusOK, samples)
}

func parseTimeParam(r *http.Request, name string, def time.Time) time.Time {
	raw := r.URL.Query().Get(name)
	if raw == "" {
		return def
	}
	if t, err := time.Parse(time.RFC3339, raw); err == nil {
		return t.UTC()
	}
	return def
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
