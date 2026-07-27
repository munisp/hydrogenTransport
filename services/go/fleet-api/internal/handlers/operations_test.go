package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/pashagolub/pgxmock/v4"
)

// GET /v1/maintenance/predictions?min_risk= must actually filter on
// risk_score (the PWA sends min_risk; it was previously ignored).
func TestListPredictions_MinRiskFilter(t *testing.T) {
	h, pool := newMockHandler(t)

	created := time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC)
	pool.ExpectQuery(`WHERE risk_score >= \$1`).WithArgs(0.7).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "bus_id", "component", "risk_score", "predicted_failure_at", "model_version", "created_at",
		}).AddRow("p1", "11111111-1111-1111-1111-111111111111", "fuel-cell", 0.9, nil, "lstm-1", created))

	rec := httptest.NewRecorder()
	h.ListPredictions(rec, httptest.NewRequest(http.MethodGet, "/v1/maintenance/predictions?min_risk=0.7", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("got %d, want 200 (body: %s)", rec.Code, rec.Body)
	}
	var body struct {
		Predictions []Prediction `json:"predictions"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if len(body.Predictions) != 1 || body.Predictions[0].RiskScore != 0.9 {
		t.Fatalf("unexpected predictions: %+v", body.Predictions)
	}
	if err := pool.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet db expectations: %v", err)
	}
}

// bus_id and min_risk combine with AND.
func TestListPredictions_BusIDAndMinRisk(t *testing.T) {
	h, pool := newMockHandler(t)

	pool.ExpectQuery(`WHERE bus_id = \$1 AND risk_score >= \$2`).
		WithArgs("11111111-1111-1111-1111-111111111111", 0.5).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "bus_id", "component", "risk_score", "predicted_failure_at", "model_version", "created_at",
		}))

	rec := httptest.NewRecorder()
	h.ListPredictions(rec, httptest.NewRequest(http.MethodGet,
		"/v1/maintenance/predictions?bus_id=11111111-1111-1111-1111-111111111111&min_risk=0.5", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("got %d, want 200 (body: %s)", rec.Code, rec.Body)
	}
	if err := pool.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet db expectations: %v", err)
	}
}

// An out-of-range or non-numeric min_risk is rejected before hitting the DB.
func TestListPredictions_InvalidMinRisk(t *testing.T) {
	for _, raw := range []string{"abc", "-0.1", "1.5"} {
		h, pool := newMockHandler(t)
		rec := httptest.NewRecorder()
		h.ListPredictions(rec, httptest.NewRequest(http.MethodGet, "/v1/maintenance/predictions?min_risk="+raw, nil))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("min_risk=%s: got %d, want 400", raw, rec.Code)
		}
		if err := pool.ExpectationsWereMet(); err != nil {
			t.Fatalf("min_risk=%s: db must not be queried: %v", raw, err)
		}
	}
}

// GET /v1/fuel/levels (0008): h2 fields stay verbatim; the generic energy
// fields ride alongside and the range estimate follows the generic level.
func TestListFuelLevels_EnergyVectors(t *testing.T) {
	h, pool := newMockHandler(t)

	ts := time.Date(2026, 7, 26, 9, 0, 0, 0, time.UTC)
	learned := 7.5
	pool.ExpectQuery(`FROM fleet\.telemetry`).
		WillReturnRows(pgxmock.NewRows([]string{
			"bus_id", "fleet_no", "energy_type", "h2_level_pct", "energy_level_pct",
			"h2_capacity_kg", "ts", "kg_per_100km",
		}).
			// H2 bus: generic mirrors h2.
			AddRow("11111111-1111-1111-1111-111111111111", "H2-001", "h2", 60.0, 60.0, 40.0, ts, &learned).
			// Battery bus: no h2 columns, generic carries SOC%.
			AddRow("22222222-2222-2222-2222-222222222222", "EV-010", "battery", 0.0, 82.0, 300.0, ts, nil))

	rec := httptest.NewRecorder()
	h.ListFuelLevels(rec, httptest.NewRequest(http.MethodGet, "/v1/fuel/levels", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("got %d, want 200 (body: %s)", rec.Code, rec.Body)
	}
	var body struct {
		FuelLevels []FuelLevel `json:"fuel_levels"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if len(body.FuelLevels) != 2 {
		t.Fatalf("got %d levels, want 2", len(body.FuelLevels))
	}

	h2 := body.FuelLevels[0]
	if h2.EnergyType != "h2" || h2.H2LevelPct != 60.0 || h2.EnergyLevelPct != 60.0 ||
		h2.H2RemainingKg != 24.0 || h2.EnergyRemaining != 24.0 {
		t.Fatalf("h2 row wrong: %+v", h2)
	}
	if h2.ConsumptionSource != "learned" || h2.ConsumptionKgPer100Km != 7.5 {
		t.Fatalf("learned rate not used: %+v", h2)
	}
	if got, want := h2.EstimatedRangeKm, 24.0*100/7.5; got != want {
		t.Fatalf("range = %v, want %v", got, want)
	}

	ev := body.FuelLevels[1]
	if ev.EnergyType != "battery" || ev.EnergyLevelPct != 82.0 ||
		ev.EnergyRemaining < 245.9 || ev.EnergyRemaining > 246.1 {
		t.Fatalf("battery row wrong: %+v", ev)
	}
	if ev.ConsumptionSource != "default" {
		t.Fatalf("battery bus without learned rate must fall back to default: %+v", ev)
	}
	if err := pool.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet db expectations: %v", err)
	}
}
