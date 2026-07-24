package handlers

import (
	"net/http"
	"time"
)

// Prediction mirrors fleet.maintenance_predictions.
type Prediction struct {
	ID                 string     `json:"id"`
	BusID              string     `json:"bus_id"`
	Component          string     `json:"component"`
	RiskScore          float64    `json:"risk_score"`
	PredictedFailureAt *time.Time `json:"predicted_failure_at,omitempty"`
	ModelVersion       string     `json:"model_version"`
	CreatedAt          time.Time  `json:"created_at"`
}

// ListPredictions handles GET /v1/maintenance/predictions?bus_id=
// (predictive-maintenance module).
func (h *Handler) ListPredictions(w http.ResponseWriter, r *http.Request) {
	query := `
		SELECT id, bus_id, component, COALESCE(risk_score,0), predicted_failure_at,
		       COALESCE(model_version,''), created_at
		FROM fleet.maintenance_predictions`
	args := []any{}
	if busID := r.URL.Query().Get("bus_id"); busID != "" {
		query += ` WHERE bus_id = $1`
		args = append(args, busID)
	}
	query += ` ORDER BY risk_score DESC NULLS LAST, created_at DESC LIMIT 200`

	rows, err := h.db.Query(r.Context(), query, args...)
	if err != nil {
		h.internal(w, "list predictions", err)
		return
	}
	defer rows.Close()

	predictions := []Prediction{}
	for rows.Next() {
		var p Prediction
		if err := rows.Scan(&p.ID, &p.BusID, &p.Component, &p.RiskScore,
			&p.PredictedFailureAt, &p.ModelVersion, &p.CreatedAt); err != nil {
			h.internal(w, "scan prediction", err)
			return
		}
		predictions = append(predictions, p)
	}
	if err := rows.Err(); err != nil {
		h.internal(w, "iterate predictions", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"predictions": predictions})
}

// h2KgPer100Km is the fleet-average H2 consumption used for range prediction
// (rule-based fallback per SPEC §4 until ML estimates are available).
const h2KgPer100Km = 8.0

// FuelLevel is the latest H2 reading per vehicle (fuel-monitoring module).
type FuelLevel struct {
	BusID            string    `json:"bus_id"`
	FleetNo          string    `json:"fleet_no"`
	H2LevelPct       float64   `json:"h2_level_pct"`
	H2RemainingKg    float64   `json:"h2_remaining_kg"`
	EstimatedRangeKm float64   `json:"estimated_range_km"`
	MeasuredAt       time.Time `json:"measured_at"`
}

// ListFuelLevels handles GET /v1/fuel/levels: latest H2 level per vehicle plus
// a deterministic range estimate (remaining kg × 100 / 8 kg per 100 km).
func (h *Handler) ListFuelLevels(w http.ResponseWriter, r *http.Request) {
	rows, err := h.db.Query(r.Context(), `
		SELECT DISTINCT ON (t.bus_id)
		       t.bus_id, v.fleet_no, COALESCE(t.h2_level_pct,0), COALESCE(v.h2_capacity_kg,0), t.ts
		FROM fleet.telemetry t
		JOIN fleet.vehicles v ON v.id = t.bus_id
		ORDER BY t.bus_id, t.ts DESC`)
	if err != nil {
		h.internal(w, "list fuel levels", err)
		return
	}
	defer rows.Close()

	levels := []FuelLevel{}
	for rows.Next() {
		var (
			busID, fleetNo string
			pct, capacity  float64
			ts             time.Time
		)
		if err := rows.Scan(&busID, &fleetNo, &pct, &capacity, &ts); err != nil {
			h.internal(w, "scan fuel level", err)
			return
		}
		remainingKg := pct / 100 * capacity
		levels = append(levels, FuelLevel{
			BusID:            busID,
			FleetNo:          fleetNo,
			H2LevelPct:       pct,
			H2RemainingKg:    remainingKg,
			EstimatedRangeKm: remainingKg * 100 / h2KgPer100Km,
			MeasuredAt:       ts,
		})
	}
	if err := rows.Err(); err != nil {
		h.internal(w, "iterate fuel levels", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"fuel_levels": levels})
}
