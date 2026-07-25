package handlers

import (
	"encoding/json"
	"errors"
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
// order, the aggregation mapping, and the honesty contract: fleet_uptime_pct
// is null (no time-based source), the static status mix is reported
// separately as fleet_active_ratio_pct.
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
	if k.Revenue30dMinor == nil || *k.Revenue30dMinor != 125000 {
		t.Fatalf("revenue: %+v", k)
	}
	if k.SettledPayments30d == nil || *k.SettledPayments30d != 42 {
		t.Fatalf("settled payments: %+v", k)
	}
	if k.RidershipEstimate30d == nil || *k.RidershipEstimate30d != 42 {
		t.Fatalf("ridership estimate must equal settled payments: %+v", k)
	}
	if k.KgCO2AvoidedTotal == nil || *k.KgCO2AvoidedTotal != 1200.5 {
		t.Fatalf("carbon kg: %+v", k)
	}
	if k.VehiclesTotal == nil || *k.VehiclesTotal != 50 || k.VehiclesActive == nil || *k.VehiclesActive != 47 {
		t.Fatalf("fleet counts: %+v", k)
	}
	if k.FleetActiveRatioPct == nil || *k.FleetActiveRatioPct != 94.0 {
		t.Fatalf("fleet_active_ratio_pct must be 94.0: %+v", k)
	}
	if k.FleetUptimePct != nil {
		t.Fatalf("fleet_uptime_pct must be null until a time-based source exists, got %v", *k.FleetUptimePct)
	}
	if k.FleetUptimeNote == "" {
		t.Fatal("fleet_uptime_note must explain the null uptime")
	}
	if k.OpenIncidents == nil || *k.OpenIncidents != 3 {
		t.Fatalf("open incidents: %+v", k)
	}
	if k.Partial || len(k.Degraded) != 0 {
		t.Fatalf("clean rollup must not be marked partial: %+v", k)
	}
	if err := pool.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet db expectations: %v", err)
	}
}

// The open-incident KPI must count incidents being worked (in_progress),
// not just open/acknowledged (audit defect). Pinned via the SQL text.
func TestGetGovKPIs_IncidentsIncludeInProgress(t *testing.T) {
	h, pool := newMockHandler(t)

	pool.ExpectQuery(`FROM commerce\.fare_payments`).
		WillReturnRows(pgxmock.NewRows([]string{"revenue", "count"}).AddRow(int64(0), int64(0)))
	pool.ExpectQuery(`FROM citizen\.carbon_credits`).
		WillReturnRows(pgxmock.NewRows([]string{"kg", "credits"}).AddRow(0.0, 0.0))
	pool.ExpectQuery(`FROM fleet\.vehicles`).
		WillReturnRows(pgxmock.NewRows([]string{"total", "active"}).AddRow(int64(0), int64(0)))
	pool.ExpectQuery(`FROM infra\.stations`).
		WillReturnRows(pgxmock.NewRows([]string{"kg"}).AddRow(0.0))
	pool.ExpectQuery(`FROM infra\.incidents WHERE status IN \('open','acknowledged','in_progress'\)`).
		WillReturnRows(pgxmock.NewRows([]string{"open"}).AddRow(int64(7)))

	rec := httptest.NewRecorder()
	h.GetGovKPIs(rec, httptest.NewRequest(http.MethodGet, "/v1/gov/kpis", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("got %d, want 200 (body: %s)", rec.Code, rec.Body)
	}
	var k GovKPIs
	if err := json.Unmarshal(rec.Body.Bytes(), &k); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if k.OpenIncidents == nil || *k.OpenIncidents != 7 {
		t.Fatalf("open incidents: %+v", k)
	}
	if err := pool.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet db expectations: %v", err)
	}
}

// A failed rollup must degrade honestly: its fields are null, the source is
// named in degraded, and the response stays 200 (no fabricated values, no
// whole-endpoint 500).
func TestGetGovKPIs_PartialDegradation(t *testing.T) {
	h, pool := newMockHandler(t)

	pool.ExpectQuery(`FROM commerce\.fare_payments`).
		WillReturnError(errors.New("connection reset"))
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
		t.Fatalf("degraded rollup must still be 200, got %d (body: %s)", rec.Code, rec.Body)
	}
	var k GovKPIs
	if err := json.Unmarshal(rec.Body.Bytes(), &k); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if k.Revenue30dMinor != nil || k.SettledPayments30d != nil || k.RidershipEstimate30d != nil {
		t.Fatalf("failed payments rollup must be null, not fabricated: %+v", k)
	}
	if !k.Partial || len(k.Degraded) != 1 || k.Degraded[0] != "fare-payments" {
		t.Fatalf("partial/degraded must name the failed source: %+v", k)
	}
	if k.KgCO2AvoidedTotal == nil || k.OpenIncidents == nil {
		t.Fatalf("healthy sections must still be reported: %+v", k)
	}
	if err := pool.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet db expectations: %v", err)
	}
}

// Zero fleet: no division by zero and no fabricated 0%/100% — the ratio is
// honestly null when there is no denominator.
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
	if k.FleetActiveRatioPct != nil {
		t.Fatalf("ratio with zero fleet must be null (no denominator), got %v", *k.FleetActiveRatioPct)
	}
	if k.FleetUptimePct != nil {
		t.Fatalf("uptime must be null, got %v", *k.FleetUptimePct)
	}
	if err := pool.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet db expectations: %v", err)
	}
}
