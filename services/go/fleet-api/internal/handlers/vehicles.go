// Package handlers implements the Domain 1 (fleet) API (SPEC §3.4 fleet schema).
package handlers

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/zap"
)

// Handler serves the fleet endpoints.
type Handler struct {
	db  DB
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

// TelemetrySample is the latest telemetry point for one bus (telematics
// module) — a telemetry point plus the bus identity.
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

func (h *Handler) internal(w http.ResponseWriter, op string, err error) {
	h.log.Error(op, zap.Error(err))
	writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
