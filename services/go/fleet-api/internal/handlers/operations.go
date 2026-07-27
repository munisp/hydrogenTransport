package handlers

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
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

// ListPredictions handles GET /v1/maintenance/predictions?bus_id=&min_risk=
// (predictive-maintenance module). min_risk is a 0..1 inclusive lower bound
// on risk_score; the PWA MaintenancePage sends it (it was previously
// accepted and silently ignored — BUSINESS_LOGIC_AUDIT dead client calls).
func (h *Handler) ListPredictions(w http.ResponseWriter, r *http.Request) {
	query := `
		SELECT id, bus_id, component, COALESCE(risk_score,0), predicted_failure_at,
		       COALESCE(model_version,''), created_at
		FROM fleet.maintenance_predictions`
	args := []any{}
	conds := []string{}
	if busID := r.URL.Query().Get("bus_id"); busID != "" {
		args = append(args, busID)
		conds = append(conds, fmt.Sprintf("bus_id = $%d", len(args)))
	}
	if raw := r.URL.Query().Get("min_risk"); raw != "" {
		minRisk, err := strconv.ParseFloat(raw, 64)
		if err != nil || minRisk < 0 || minRisk > 1 {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "min_risk must be a number between 0 and 1"})
			return
		}
		args = append(args, minRisk)
		conds = append(conds, fmt.Sprintf("risk_score >= $%d", len(args)))
	}
	if len(conds) > 0 {
		query += ` WHERE ` + strings.Join(conds, ` AND `)
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

// defaultH2KgPer100Km is the fallback H2 consumption used for range
// prediction only until fleet.fuel_consumption holds a per-bus rate learned
// from fuel.reading events (0007; BUSINESS_LOGIC_AUDIT §4: one fleet-wide
// constant was used for every bus).
const defaultH2KgPer100Km = 8.0

// FuelLevel is the latest energy reading per vehicle (fuel-monitoring
// module). H2 fields are kept verbatim; the Wave-5 generic fields mirror
// them for H2 buses and carry native readings for battery/diesel/cng buses.
type FuelLevel struct {
	BusID         string  `json:"bus_id"`
	FleetNo       string  `json:"fleet_no"`
	EnergyType    string  `json:"energy_type"` // h2|battery|diesel|cng (0008)
	H2LevelPct    float64 `json:"h2_level_pct"`
	H2RemainingKg float64 `json:"h2_remaining_kg"`
	// EnergyLevelPct is the generic level (energy_level_pct falling back to
	// h2_level_pct) and EnergyRemaining the remaining energy in the bus's
	// native unit (kg for h2/cng, kwh for battery, liters for diesel — the
	// capacity column is shared; EnergyType names the unit domain).
	EnergyLevelPct   float64 `json:"energy_level_pct"`
	EnergyRemaining  float64 `json:"energy_remaining"`
	EstimatedRangeKm float64 `json:"estimated_range_km"`
	// ConsumptionKgPer100Km is the rate behind the range estimate and
	// ConsumptionSource says whether it was learned from fuel.reading
	// telemetry or is the fleet-wide default. The learned rate is per-bus
	// (fleet.fuel_consumption), so it is already keyed per energy_type
	// implicitly: a bus has exactly one energy vector.
	ConsumptionKgPer100Km float64   `json:"consumption_kg_per_100km"`
	ConsumptionSource     string    `json:"consumption_source"` // learned|default
	MeasuredAt            time.Time `json:"measured_at"`
}

// ListFuelLevels handles GET /v1/fuel/levels: latest energy level per vehicle
// plus a deterministic range estimate (remaining × 100 / rate per 100 km).
func (h *Handler) ListFuelLevels(w http.ResponseWriter, r *http.Request) {
	rows, err := h.db.Query(r.Context(), `
		SELECT DISTINCT ON (t.bus_id)
		       t.bus_id, v.fleet_no, COALESCE(v.energy_type,'h2'),
		       COALESCE(t.h2_level_pct,0),
		       COALESCE(t.energy_level_pct, t.h2_level_pct, 0),
		       COALESCE(v.h2_capacity_kg,0), t.ts,
		       fc.kg_per_100km
		FROM fleet.telemetry t
		JOIN fleet.vehicles v ON v.id = t.bus_id
		LEFT JOIN fleet.fuel_consumption fc ON fc.bus_id = t.bus_id
		ORDER BY t.bus_id, t.ts DESC`)
	if err != nil {
		h.internal(w, "list fuel levels", err)
		return
	}
	defer rows.Close()

	levels := []FuelLevel{}
	for rows.Next() {
		var (
			busID, fleetNo, energyType string
			pct, energyPct, capacity   float64
			ts                         time.Time
			learned                    *float64
		)
		if err := rows.Scan(&busID, &fleetNo, &energyType, &pct, &energyPct, &capacity, &ts, &learned); err != nil {
			h.internal(w, "scan fuel level", err)
			return
		}
		remainingKg := pct / 100 * capacity
		energyRemaining := energyPct / 100 * capacity
		rate, source := defaultH2KgPer100Km, "default"
		if learned != nil && *learned > 0 {
			rate, source = *learned, "learned"
		}
		levels = append(levels, FuelLevel{
			BusID:                 busID,
			FleetNo:               fleetNo,
			EnergyType:            energyType,
			H2LevelPct:            pct,
			H2RemainingKg:         remainingKg,
			EnergyLevelPct:        energyPct,
			EnergyRemaining:       energyRemaining,
			EstimatedRangeKm:      energyRemaining * 100 / rate,
			ConsumptionKgPer100Km: rate,
			ConsumptionSource:     source,
			MeasuredAt:            ts,
		})
	}
	if err := rows.Err(); err != nil {
		h.internal(w, "iterate fuel levels", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"fuel_levels": levels})
}
