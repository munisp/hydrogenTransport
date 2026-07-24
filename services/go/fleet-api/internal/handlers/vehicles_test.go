package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
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

// GET /v1/vehicles must return the {"vehicles": [...]} envelope with the
// fleet.vehicles column mapping intact (SPEC §3.4).
func TestListVehicles_Shape(t *testing.T) {
	h, pool := newMockHandler(t)

	lat, lon := 52.52, 13.405
	pool.ExpectQuery(`FROM fleet\.vehicles`).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "fleet_no", "vin", "model", "h2_capacity_kg", "status", "lat", "lon",
		}).
			AddRow("11111111-1111-1111-1111-111111111111", "H2-001", "VIN0001", "VanHool A330 FC", 37.5, "active", &lat, &lon).
			AddRow("22222222-2222-2222-2222-222222222222", "H2-002", "VIN0002", "Solaris Urbino H2", 32.0, "depot", nil, nil))

	rec := httptest.NewRecorder()
	h.ListVehicles(rec, httptest.NewRequest(http.MethodGet, "/v1/vehicles", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("got %d, want 200 (body: %s)", rec.Code, rec.Body)
	}
	var body struct {
		Vehicles []Vehicle `json:"vehicles"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if len(body.Vehicles) != 2 {
		t.Fatalf("got %d vehicles, want 2", len(body.Vehicles))
	}
	v := body.Vehicles[0]
	if v.FleetNo != "H2-001" || v.VIN != "VIN0001" || v.Model != "VanHool A330 FC" ||
		v.H2CapacityKg != 37.5 || v.Status != "active" {
		t.Fatalf("vehicle mapping wrong: %+v", v)
	}
	if v.Lat == nil || *v.Lat != 52.52 || v.Lon == nil || *v.Lon != 13.405 {
		t.Fatalf("geom not mapped to lat/lon: %+v", v)
	}
	// omitempty: a vehicle without position must not carry lat/lon keys.
	if strings.Contains(string(rec.Body.Bytes()), `"fleet_no":"H2-002"`) &&
		strings.Contains(strings.Split(string(rec.Body.Bytes()), `"fleet_no":"H2-002"`)[1], `"lat"`) {
		t.Fatalf("nil position must be omitted, body: %s", rec.Body)
	}
	if err := pool.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet db expectations: %v", err)
	}
}

// GET /v1/telemetry/latest must return a JSON array (not an envelope) of
// per-bus latest samples — the shape the E2E smoke check relies on.
func TestLatestTelemetry_Shape(t *testing.T) {
	h, pool := newMockHandler(t)

	ts := time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC)
	pool.ExpectQuery(`DISTINCT ON \(bus_id\)`).
		WillReturnRows(pgxmock.NewRows([]string{
			"bus_id", "ts", "speed_kph", "h2_level_pct", "fuel_cell_kw",
			"battery_soc_pct", "odometer_km", "lat", "lon",
		}).AddRow("11111111-1111-1111-1111-111111111111", ts, 42.5, 63.0, 55.0, 81.0, 12345.6, nil, nil))

	rec := httptest.NewRecorder()
	h.LatestTelemetry(rec, httptest.NewRequest(http.MethodGet, "/v1/telemetry/latest", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("got %d, want 200 (body: %s)", rec.Code, rec.Body)
	}
	var samples []TelemetrySample
	if err := json.Unmarshal(rec.Body.Bytes(), &samples); err != nil {
		t.Fatalf("body must be a bare JSON array: %v (body: %s)", err, rec.Body)
	}
	if len(samples) != 1 || samples[0].BusID != "11111111-1111-1111-1111-111111111111" ||
		samples[0].SpeedKph != 42.5 || samples[0].H2LevelPct != 63.0 {
		t.Fatalf("sample mapping wrong: %+v", samples)
	}
	if err := pool.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet db expectations: %v", err)
	}
}

// GET /v1/vehicles/{id} must 404 (not 500) on an unknown id.
func TestGetVehicle_NotFound(t *testing.T) {
	h, pool := newMockHandler(t)
	pool.ExpectQuery(`WHERE id = \$1`).WithArgs("nope").WillReturnError(pgx.ErrNoRows)

	r := chi.NewRouter()
	r.Get("/v1/vehicles/{id}", h.GetVehicle)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/vehicles/nope", nil))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("got %d, want 404 (body: %s)", rec.Code, rec.Body)
	}
	if err := pool.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet db expectations: %v", err)
	}
}

// A database failure must surface as 500 with a generic error body (no leak
// of the underlying driver error).
func TestListVehicles_DBError(t *testing.T) {
	h, pool := newMockHandler(t)
	pool.ExpectQuery(`FROM fleet\.vehicles`).WillReturnError(pgx.ErrTxClosed)

	rec := httptest.NewRecorder()
	h.ListVehicles(rec, httptest.NewRequest(http.MethodGet, "/v1/vehicles", nil))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("got %d, want 500", rec.Code)
	}
	if err := pool.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet db expectations: %v", err)
	}
}
