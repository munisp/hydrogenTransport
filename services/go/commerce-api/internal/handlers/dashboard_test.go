package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/pashagolub/pgxmock/v4"
	"go.uber.org/zap"
)

func newMockHandler(t *testing.T) (*Handler, pgxmock.PgxPoolIface) {
	t.Helper()
	pool, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock.NewPool: %v", err)
	}
	t.Cleanup(pool.Close)
	return &Handler{db: pool, log: zap.NewNop()}, pool
}

// GET /v1/gov/kpis rolls up all four domain schemas; the test pins the SQL
// order, the aggregation mapping, and the derived uptime percentage.
func TestGetGovKPIs(t *testing.T) {
	h, pool := newMockHandler(t)

	pool.ExpectQuery(`FROM commerce\.fare_payments`).
		WillReturnRows(pgxmock.NewRows([]string{"revenue", "count"}).AddRow(int64(125000), int64(42)))
	pool.ExpectQuery(`FROM citizen\.carbon_credits`).
		WillReturnRows(pgxmock.NewRows([]string{"kg", "credits"}).AddRow(1200.5, 34.0))
	pool.ExpectQuery(`FROM fleet\.vehicles`).
		WillReturnRows(pgxmock.NewRows([]string{"total", "active"}).AddRow(int64(50), int64(47)))
	pool.ExpectQuery(`FROM infra\.stations`).
		WillReturnRows(pgxmock.NewRows([]string{"kg"}).AddRow(820.25))
	pool.ExpectQuery(`FROM infra\.incidents`).
		WillReturnRows(pgxmock.NewRows([]string{"open"}).AddRow(int64(3)))

	rec := httptest.NewRecorder()
	h.GetGovKPIs(rec, httptest.NewRequest(http.MethodGet, "/v1/gov/kpis", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("got %d, want 200 (body: %s)", rec.Code, rec.Body)
	}
	var k GovKPIs
	if err := json.Unmarshal(rec.Body.Bytes(), &k); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	want := GovKPIs{
		Revenue30dMinor:      125000,
		SettledPayments30d:   42,
		RidershipEstimate30d: 42, // one settled fare ≈ one ride
		KgCO2AvoidedTotal:    1200.5,
		CarbonCreditsTotal:   34.0,
		VehiclesTotal:        50,
		VehiclesActive:       47,
		FleetUptimePct:       94.0,
		StationsAvailableKg:  820.25,
		OpenIncidents:        3,
	}
	if k != want {
		t.Fatalf("KPIs mismatch:\n got %+v\nwant %+v", k, want)
	}
	if err := pool.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet db expectations: %v", err)
	}
}

// Zero vehicles must not divide by zero; uptime stays 0.
func TestGetGovKPIs_ZeroFleet(t *testing.T) {
	h, pool := newMockHandler(t)

	pool.ExpectQuery(`FROM commerce\.fare_payments`).
		WillReturnRows(pgxmock.NewRows([]string{"revenue", "count"}).AddRow(int64(0), int64(0)))
	pool.ExpectQuery(`FROM citizen\.carbon_credits`).
		WillReturnRows(pgxmock.NewRows([]string{"kg", "credits"}).AddRow(0.0, 0.0))
	pool.ExpectQuery(`FROM fleet\.vehicles`).
		WillReturnRows(pgxmock.NewRows([]string{"total", "active"}).AddRow(int64(0), int64(0)))
	pool.ExpectQuery(`FROM infra\.stations`).
		WillReturnRows(pgxmock.NewRows([]string{"kg"}).AddRow(0.0))
	pool.ExpectQuery(`FROM infra\.incidents`).
		WillReturnRows(pgxmock.NewRows([]string{"open"}).AddRow(int64(0)))

	rec := httptest.NewRecorder()
	h.GetGovKPIs(rec, httptest.NewRequest(http.MethodGet, "/v1/gov/kpis", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("got %d, want 200 (body: %s)", rec.Code, rec.Body)
	}
	var k GovKPIs
	if err := json.Unmarshal(rec.Body.Bytes(), &k); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if k.FleetUptimePct != 0 {
		t.Fatalf("uptime with zero fleet must be 0, got %v", k.FleetUptimePct)
	}
	if err := pool.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet db expectations: %v", err)
	}
}
