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
