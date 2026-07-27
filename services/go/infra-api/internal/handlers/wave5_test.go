package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/pashagolub/pgxmock/v4"
)

var stationRowCols = []string{
	"id", "name", "capacity_kg", "available_kg", "status",
	"station_type", "available_kwh", "charger_count", "lat", "lon",
}

// GET /v1/stations (0008): stations carry station_type + EV inventory fields;
// H2 stations have NULL available_kwh/charger_count (omitted from JSON).
func TestListStations_EnergyFields(t *testing.T) {
	h, pool := newMockHandler(t)

	kwh, chargers := 1200.0, 8
	pool.ExpectQuery(`FROM infra\.stations`).
		WillReturnRows(pgxmock.NewRows(stationRowCols).
			AddRow("11111111-1111-1111-1111-111111111111", "Depot H2", 1000.0, 500.0, "online", "h2", nil, nil, nil, nil).
			AddRow("22222222-2222-2222-2222-222222222222", "EV Hub", 0.0, 0.0, "online", "ev_charger", &kwh, &chargers, nil, nil))

	rec := httptest.NewRecorder()
	h.ListStations(rec, httptest.NewRequest(http.MethodGet, "/v1/stations", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("got %d, want 200 (body: %s)", rec.Code, rec.Body)
	}
	var body struct {
		Stations []Station `json:"stations"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Stations) != 2 {
		t.Fatalf("got %d stations, want 2", len(body.Stations))
	}
	h2, ev := body.Stations[0], body.Stations[1]
	if h2.StationType != "h2" || h2.AvailableKwh != nil || h2.ChargerCount != nil {
		t.Fatalf("h2 station wrong: %+v", h2)
	}
	if ev.StationType != "ev_charger" || ev.AvailableKwh == nil || *ev.AvailableKwh != 1200.0 ||
		ev.ChargerCount == nil || *ev.ChargerCount != 8 {
		t.Fatalf("ev station wrong: %+v", ev)
	}
	if err := pool.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet db expectations: %v", err)
	}
}

// POST /v1/stations rejects an unknown station_type before touching the DB
// (the CHECK constraint domain is validated in the API for a clean 400).
func TestCreateStation_InvalidStationType(t *testing.T) {
	h, pool := newMockHandler(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/stations",
		strings.NewReader(`{"name":"X","station_type":"petrol"}`))
	h.CreateStation(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("got %d, want 400 (body: %s)", rec.Code, rec.Body)
	}
	if err := pool.ExpectationsWereMet(); err != nil {
		t.Fatalf("db must not be queried: %v", err)
	}
}

// POST /v1/stations creates an EV charger station with kWh inventory and
// charger count (station_type defaults to h2 when omitted — tested above
// implicitly; here the explicit ev_charger path).
func TestCreateStation_EVCharger(t *testing.T) {
	h, pool := newMockHandler(t)

	kwh, chargers := 800.0, 4
	pool.ExpectQuery(`INSERT INTO infra\.stations`).
		WithArgs("EV Hub", 0.0, 0.0, "online", "ev_charger", &kwh, &chargers, nil).
		WillReturnRows(pgxmock.NewRows(stationRowCols).
			AddRow("22222222-2222-2222-2222-222222222222", "EV Hub", 0.0, 0.0, "online", "ev_charger", &kwh, &chargers, nil, nil))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/stations",
		strings.NewReader(`{"name":"EV Hub","station_type":"ev_charger","available_kwh":800,"charger_count":4}`))
	h.CreateStation(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("got %d, want 201 (body: %s)", rec.Code, rec.Body)
	}
	var s Station
	if err := json.Unmarshal(rec.Body.Bytes(), &s); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if s.StationType != "ev_charger" || s.AvailableKwh == nil || *s.AvailableKwh != 800.0 {
		t.Fatalf("unexpected station: %+v", s)
	}
	if err := pool.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet db expectations: %v", err)
	}
}

var chargePointRowCols = []string{
	"id", "station_id", "ocpp_id", "vendor", "model", "status", "last_heartbeat", "created_at",
}

// GET /v1/stations/{id}/chargers returns the station's OCPP charge points
// with live status (rows are written by the ocpp-gateway).
func TestListStationChargers(t *testing.T) {
	h, pool := newMockHandler(t)

	station := "11111111-1111-1111-1111-111111111111"
	hb := time.Date(2026, 7, 26, 10, 0, 0, 0, time.UTC)
	pool.ExpectQuery(`FROM infra\.charge_points WHERE station_id = \$1`).
		WithArgs(station).
		WillReturnRows(pgxmock.NewRows(chargePointRowCols).
			AddRow("cp-1", station, "CP-0001", "ABB", "Terra 184", "Charging", &hb, hb))

	rec := httptest.NewRecorder()
	req := withURLParams(httptest.NewRequest(http.MethodGet, "/v1/stations/"+station+"/chargers", nil), "id", station)
	h.ListStationChargers(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("got %d, want 200 (body: %s)", rec.Code, rec.Body)
	}
	var body struct {
		ChargePoints []ChargePoint `json:"charge_points"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.ChargePoints) != 1 || body.ChargePoints[0].OcppID != "CP-0001" ||
		body.ChargePoints[0].Status != "Charging" {
		t.Fatalf("unexpected charge points: %+v", body.ChargePoints)
	}
	if err := pool.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet db expectations: %v", err)
	}
}

// GET /v1/chargers lists the fleet-wide inventory; ?station_id= filters.
func TestListChargers_FilterByStation(t *testing.T) {
	h, pool := newMockHandler(t)

	station := "11111111-1111-1111-1111-111111111111"
	ts := time.Date(2026, 7, 26, 10, 0, 0, 0, time.UTC)
	pool.ExpectQuery(`FROM infra\.charge_points WHERE station_id = \$1`).
		WithArgs(station).
		WillReturnRows(pgxmock.NewRows(chargePointRowCols).
			AddRow("cp-1", station, "CP-0001", "ABB", "Terra 184", "Available", nil, ts))

	rec := httptest.NewRecorder()
	h.ListChargers(rec, httptest.NewRequest(http.MethodGet, "/v1/chargers?station_id="+station, nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("got %d, want 200 (body: %s)", rec.Code, rec.Body)
	}
	if err := pool.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet db expectations: %v", err)
	}
}

// GET /v1/chargers/{ocpp_id}/sessions returns charging sessions newest
// first (?status= composes with the ocpp_id filter).
func TestListChargerSessions(t *testing.T) {
	h, pool := newMockHandler(t)

	started := time.Date(2026, 7, 26, 9, 0, 0, 0, time.UTC)
	stopped := started.Add(90 * time.Minute)
	kwh, meterStop := 54.5, 1254.5
	busID := "BUS-EV-010"
	pool.ExpectQuery(`FROM infra\.charging_sessions s`).
		WithArgs("CP-0001", "completed").
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "charge_point_id", "bus_id", "connector_id", "id_tag",
			"meter_start", "meter_stop", "kwh", "started_at", "stopped_at", "status",
		}).AddRow("s-1", "cp-1", &busID, 1, nil, 1200.0, &meterStop, &kwh, started, &stopped, "completed"))

	rec := httptest.NewRecorder()
	req := withURLParams(httptest.NewRequest(http.MethodGet, "/v1/chargers/CP-0001/sessions?status=completed", nil),
		"ocpp_id", "CP-0001")
	h.ListChargerSessions(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("got %d, want 200 (body: %s)", rec.Code, rec.Body)
	}
	var body struct {
		Sessions []ChargingSession `json:"sessions"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Sessions) != 1 || body.Sessions[0].Kwh == nil || *body.Sessions[0].Kwh != 54.5 ||
		body.Sessions[0].Status != "completed" {
		t.Fatalf("unexpected sessions: %+v", body.Sessions)
	}
	if err := pool.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet db expectations: %v", err)
	}
}

// Completing a queue entry at an ev_charger station draws down available_kwh
// (not available_kg) and names the kwh unit in the response (0008).
func TestCompleteStationQueueEntry_EVChargerDrawDown(t *testing.T) {
	h, pool := newMockHandler(t)

	joined := time.Date(2026, 7, 26, 11, 0, 0, 0, time.UTC)
	station := "11111111-1111-1111-1111-111111111111"
	entry := "33333333-3333-3333-3333-333333333333"
	pool.ExpectBegin()
	pool.ExpectQuery(`WITH done AS`).
		WithArgs(entry, station).
		WillReturnRows(pgxmock.NewRows(append(queueCols, "available", "station_type")).
			AddRow(entry, station, "22222222-2222-2222-2222-222222222222", joined, "completed", 0.0, "ev_charger"))
	pool.ExpectQuery(`SELECT COALESCE\(available_kwh,0\) FROM infra\.stations`).
		WithArgs(station).
		WillReturnRows(pgxmock.NewRows([]string{"available_kwh"}).AddRow(500.0))
	pool.ExpectQuery(`UPDATE infra\.stations SET available_kwh`).
		WithArgs(station, 120.0).
		WillReturnRows(pgxmock.NewRows([]string{"available_kwh"}).AddRow(380.0))
	pool.ExpectCommit()
	pool.ExpectExec(`UPDATE infra\.station_queue SET status = 'serving'`).
		WithArgs(station).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	rec := httptest.NewRecorder()
	req := withURLParams(httptest.NewRequest(http.MethodPost, "/v1/stations/"+station+"/queue/"+entry+"/complete",
		strings.NewReader(`{"dispensed_amount":120}`)), "id", station, "entry", entry)
	h.CompleteStationQueueEntry(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("got %d, want 200 (body: %s)", rec.Code, rec.Body)
	}
	var e QueueEntry
	if err := json.Unmarshal(rec.Body.Bytes(), &e); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if e.DispensedUnit != "kwh" || e.DispensedAmount == nil || *e.DispensedAmount != 120.0 ||
		e.AvailableAfter == nil || *e.AvailableAfter != 380.0 {
		t.Fatalf("kwh draw-down wrong: %+v", e)
	}
	// Legacy kg fields must stay empty for an EV station.
	if e.DispensedKg != nil || e.AvailableAfterKg != nil {
		t.Fatalf("legacy kg fields must be unset for ev_charger: %+v", e)
	}
	if err := pool.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet db expectations: %v", err)
	}
}

// An EV station that cannot cover the dispensed kWh refuses with 409 and the
// body names the kWh inventory field.
func TestCompleteStationQueueEntry_EVInsufficientKwh(t *testing.T) {
	h, pool := newMockHandler(t)

	joined := time.Date(2026, 7, 26, 11, 0, 0, 0, time.UTC)
	station := "11111111-1111-1111-1111-111111111111"
	entry := "33333333-3333-3333-3333-333333333333"
	pool.ExpectBegin()
	pool.ExpectQuery(`WITH done AS`).
		WithArgs(entry, station).
		WillReturnRows(pgxmock.NewRows(append(queueCols, "available", "station_type")).
			AddRow(entry, station, "22222222-2222-2222-2222-222222222222", joined, "completed", 0.0, "ev_charger"))
	pool.ExpectQuery(`SELECT COALESCE\(available_kwh,0\) FROM infra\.stations`).
		WithArgs(station).
		WillReturnRows(pgxmock.NewRows([]string{"available_kwh"}).AddRow(50.0))
	pool.ExpectRollback()

	rec := httptest.NewRecorder()
	req := withURLParams(httptest.NewRequest(http.MethodPost, "/v1/stations/"+station+"/queue/"+entry+"/complete",
		strings.NewReader(`{"dispensed_amount":120}`)), "id", station, "entry", entry)
	h.CompleteStationQueueEntry(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("got %d, want 409 (body: %s)", rec.Code, rec.Body)
	}
	if !strings.Contains(rec.Body.String(), "available_kwh") ||
		!strings.Contains(rec.Body.String(), "insufficient_inventory") {
		t.Fatalf("body should name insufficient available_kwh: %s", rec.Body)
	}
	if err := pool.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet db expectations: %v", err)
	}
}

// The Wave-5 compliance template packs: h2 reproduces the pre-Wave-5 report,
// battery swaps leak aging for thermal categories, diesel drops leak
// sections, cng keeps gas-leak aging over its own incident type.
func TestCompliancePacks(t *testing.T) {
	if !compliancePacks["h2"].LeakAging || len(compliancePacks["h2"].LeakTypes) != 1 ||
		compliancePacks["h2"].LeakTypes[0] != "h2_leak" {
		t.Fatalf("h2 pack changed — backward compat broken: %+v", compliancePacks["h2"])
	}
	if compliancePacks["battery"].LeakAging || !compliancePacks["battery"].BatteryThermal {
		t.Fatalf("battery pack must drop leak aging and add thermal categories: %+v", compliancePacks["battery"])
	}
	if compliancePacks["diesel"].LeakAging || compliancePacks["diesel"].BatteryThermal {
		t.Fatalf("diesel pack must drop leak sections: %+v", compliancePacks["diesel"])
	}
	if !compliancePacks["cng"].LeakAging || compliancePacks["cng"].LeakTypes[0] != "cng_leak" {
		t.Fatalf("cng pack must keep gas-leak aging: %+v", compliancePacks["cng"])
	}
	// No query param and no fleet config → h2 (backward compatible default).
	if got := defaultComplianceDomain(); got != "h2" {
		t.Fatalf("defaultComplianceDomain() = %q, want h2", got)
	}
}

// An unknown ?domain= is rejected with 400 before any report is built.
func TestGenerateComplianceReport_InvalidDomain(t *testing.T) {
	h, pool := newMockHandler(t)

	rec := httptest.NewRecorder()
	h.GenerateComplianceReport(rec, httptest.NewRequest(http.MethodPost,
		"/v1/compliance/reports/generate?domain=petrol", nil))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("got %d, want 400 (body: %s)", rec.Code, rec.Body)
	}
	if err := pool.ExpectationsWereMet(); err != nil {
		t.Fatalf("db must not be queried: %v", err)
	}
}
