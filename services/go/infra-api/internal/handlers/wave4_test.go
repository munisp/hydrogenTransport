package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/pashagolub/pgxmock/v4"
)

// withURLParams injects chi route params into a request built by
// httptest.NewRequest (handlers read {id}/{entry} via chi.URLParam).
func withURLParams(req *http.Request, kv ...string) *http.Request {
	rctx := chi.NewRouteContext()
	for i := 0; i+1 < len(kv); i += 2 {
		rctx.URLParams.Add(kv[i], kv[i+1])
	}
	return req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
}

var incidentRowCols = []string{"id", "type", "severity", "bus_id", "station_id", "status", "opened_at", "meta"}

// ppm → severity bands (BUSINESS_LOGIC_AUDIT §7: h2_ppm was never used).
func TestLeakSeverity_PpmBands(t *testing.T) {
	cases := []struct {
		caller string
		ppm    *float64
		want   string
	}{
		{"", nil, "high"},               // documented default
		{"low", nil, "low"},             // caller value kept when no ppm
		{"bogus", nil, "high"},          // unknown caller value → default
		{"low", f64(30000), "critical"}, // ppm raises the floor
		{"low", f64(6000), "high"},
		{"low", f64(1500), "medium"},
		{"high", f64(10), "high"}, // caller may escalate above the band
		{"critical", f64(10), "critical"},
		{"low", f64(10), "low"},
	}
	for _, c := range cases {
		if got := leakSeverity(c.caller, c.ppm); got != c.want {
			t.Errorf("leakSeverity(%q, %v) = %q, want %q", c.caller, c.ppm, got, c.want)
		}
	}
}

func f64(v float64) *float64 { return &v }

func strPtr(s string) *string { return &s }

// A repeat reading from a sensor with an active incident is folded into the
// existing incident (200 + deduplicated:true), never a new row.
func TestIngestLeak_DeduplicatesFlappingSensor(t *testing.T) {
	h, pool := newMockHandler(t)

	opened := time.Date(2026, 7, 25, 10, 0, 0, 0, time.UTC)
	pool.ExpectQuery(`UPDATE infra\.incidents`).
		WithArgs("sensor-7", "high", f64(6000)).
		WillReturnRows(pgxmock.NewRows(incidentRowCols).
			AddRow("aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa", "h2_leak", "high", nil, nil, "open", opened, []byte(`{}`)))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/safety/leak",
		strings.NewReader(`{"sensor_id":"sensor-7","severity":"low","h2_ppm":6000}`))
	h.IngestLeak(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("got %d, want 200 (body: %s)", rec.Code, rec.Body)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["deduplicated"] != true {
		t.Fatalf("expected deduplicated:true, got %v", body)
	}
	if err := pool.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet db expectations: %v", err)
	}
}

// A first reading opens a new incident whose severity is derived from ppm
// (caller "low" + 30,000 ppm → critical) and the workflow is signalled.
func TestIngestLeak_NewIncidentPpmSeverity(t *testing.T) {
	h, pool := newDispatchHandler(t) // has pub + wf fakes

	opened := time.Date(2026, 7, 25, 10, 0, 0, 0, time.UTC)
	pool.ExpectQuery(`UPDATE infra\.incidents`).
		WithArgs("sensor-9", "critical", f64(30000)).
		WillReturnRows(pgxmock.NewRows(incidentRowCols)) // no active incident
	pool.ExpectQuery(`INSERT INTO infra\.incidents`).
		WithArgs("h2_leak", "critical", (*string)(nil), (*string)(nil), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(incidentRowCols).
			AddRow("bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb", "h2_leak", "critical", nil, nil, "open", opened, []byte(`{"sensor_id":"sensor-9"}`)))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/safety/leak",
		strings.NewReader(`{"sensor_id":"sensor-9","severity":"low","h2_ppm":30000}`))
	h.IngestLeak(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("got %d, want 202 (body: %s)", rec.Code, rec.Body)
	}
	var body struct {
		Incident Incident `json:"incident"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Incident.Severity != "critical" {
		t.Fatalf("severity = %q, want ppm-derived critical", body.Incident.Severity)
	}
	if err := pool.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet db expectations: %v", err)
	}
}

var queueCols = []string{"id", "station_id", "bus_id", "joined_at", "status"}

// Joining a non-empty queue lands the bus at the tail with a position and an
// estimated wait derived from station service history.
func TestJoinStationQueue_Tail(t *testing.T) {
	h, pool := newMockHandler(t)

	joined := time.Date(2026, 7, 25, 11, 0, 0, 0, time.UTC)
	station := "11111111-1111-1111-1111-111111111111"
	bus := "22222222-2222-2222-2222-222222222222"
	pool.ExpectBegin()
	pool.ExpectQuery(`FROM infra\.stations`).WithArgs(station).
		WillReturnRows(pgxmock.NewRows([]string{"status"}).AddRow("online"))
	pool.ExpectQuery(`SELECT count\(\*\) FROM infra\.station_queue`).WithArgs(station).
		WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(2))
	pool.ExpectQuery(`INSERT INTO infra\.station_queue`).
		WithArgs(station, bus, "waiting").
		WillReturnRows(pgxmock.NewRows(queueCols).AddRow("33333333-3333-3333-3333-333333333333", station, bus, joined, "waiting"))
	pool.ExpectCommit()
	pool.ExpectQuery(`avg\(EXTRACT`).WithArgs(station).
		WillReturnRows(pgxmock.NewRows([]string{"avg"}).AddRow(nil)) // no history → 15 min default

	rec := httptest.NewRecorder()
	req := withURLParams(httptest.NewRequest(http.MethodPost, "/v1/stations/"+station+"/queue",
		strings.NewReader(`{"bus_id":"`+bus+`"}`)), "id", station)
	h.JoinStationQueue(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("got %d, want 201 (body: %s)", rec.Code, rec.Body)
	}
	var e QueueEntry
	if err := json.Unmarshal(rec.Body.Bytes(), &e); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if e.Status != "waiting" || e.Position != 3 || e.EstWaitMinutes != 30 {
		t.Fatalf("unexpected queue entry: %+v", e)
	}
	if err := pool.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet db expectations: %v", err)
	}
}

// Completing a serving entry with dispensed kg decrements station inventory
// and refuses to dispense more than the recorded stock.
func TestCompleteStationQueueEntry_InsufficientInventory(t *testing.T) {
	h, pool := newMockHandler(t)

	joined := time.Date(2026, 7, 25, 11, 0, 0, 0, time.UTC)
	station := "11111111-1111-1111-1111-111111111111"
	entry := "33333333-3333-3333-3333-333333333333"
	kg := 50.0
	pool.ExpectBegin()
	pool.ExpectQuery(`WITH done AS`).
		WithArgs(entry, station).
		WillReturnRows(pgxmock.NewRows(append(queueCols, "available")).
			AddRow(entry, station, "22222222-2222-2222-2222-222222222222", joined, "completed", 10.0))
	pool.ExpectRollback()

	rec := httptest.NewRecorder()
	req := withURLParams(httptest.NewRequest(http.MethodPost, "/v1/stations/"+station+"/queue/"+entry+"/complete",
		strings.NewReader(`{"dispensed_kg":50}`)), "id", station, "entry", entry)
	_ = kg
	h.CompleteStationQueueEntry(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("got %d, want 409 insufficient_inventory (body: %s)", rec.Code, rec.Body)
	}
	if !strings.Contains(rec.Body.String(), "insufficient_inventory") {
		t.Fatalf("body should name insufficient_inventory: %s", rec.Body)
	}
	if err := pool.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet db expectations: %v", err)
	}
}

var workOrderRowCols = []string{"id", "title", "description", "asset_ref", "status",
	"bus_id", "prediction_id", "assignee", "started_at", "opened_at", "closed_at"}

// The work-order lifecycle rejects illegal transitions (open → on_hold).
func TestTransitionWorkOrder_Illegal(t *testing.T) {
	h, pool := newMockHandler(t)

	id := "44444444-4444-4444-4444-444444444444"
	pool.ExpectQuery(`SELECT status FROM infra\.work_orders`).WithArgs(id).
		WillReturnRows(pgxmock.NewRows([]string{"status"}).AddRow("open"))

	rec := httptest.NewRecorder()
	req := withURLParams(httptest.NewRequest(http.MethodPost, "/v1/depot/work-orders/"+id+"/hold", nil), "id", id)
	h.HoldWorkOrder(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("got %d, want 409 (body: %s)", rec.Code, rec.Body)
	}
	if !strings.Contains(rec.Body.String(), "invalid status transition") {
		t.Fatalf("body should name the invalid transition: %s", rec.Body)
	}
	if err := pool.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet db expectations: %v", err)
	}
}

// open → assigned requires an assignee and is a legal transition.
func TestAssignWorkOrder(t *testing.T) {
	h, pool := newMockHandler(t)

	id := "44444444-4444-4444-4444-444444444444"
	opened := time.Date(2026, 7, 25, 8, 0, 0, 0, time.UTC)
	pool.ExpectQuery(`SELECT status FROM infra\.work_orders`).WithArgs(id).
		WillReturnRows(pgxmock.NewRows([]string{"status"}).AddRow("open"))
	pool.ExpectQuery(`UPDATE infra\.work_orders SET status = \$2, assignee = \$3`).
		WithArgs(id, "assigned", "tech-01").
		WillReturnRows(pgxmock.NewRows(workOrderRowCols).
			AddRow(id, "Fix compressor", "", "", "assigned", nil, nil, strPtr("tech-01"), nil, opened, nil))

	rec := httptest.NewRecorder()
	req := withURLParams(httptest.NewRequest(http.MethodPost, "/v1/depot/work-orders/"+id+"/assign",
		strings.NewReader(`{"assignee":"tech-01"}`)), "id", id)
	h.AssignWorkOrder(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("got %d, want 200 (body: %s)", rec.Code, rec.Body)
	}
	var o WorkOrder
	if err := json.Unmarshal(rec.Body.Bytes(), &o); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if o.Status != "assigned" || o.Assignee == nil || *o.Assignee != "tech-01" {
		t.Fatalf("unexpected work order: %+v", o)
	}
	if err := pool.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet db expectations: %v", err)
	}
}

// Bay occupancy: a free bay accepts a bus (occupied_by was never settable —
// BUSINESS_LOGIC_AUDIT §10); an occupied bay refuses.
func TestAssignDepotBay_NotFree(t *testing.T) {
	h, pool := newMockHandler(t)

	id := "55555555-5555-5555-5555-555555555555"
	pool.ExpectQuery(`UPDATE infra\.depot_bays`).WithArgs(id, "22222222-2222-2222-2222-222222222222").
		WillReturnRows(pgxmock.NewRows([]string{"id", "depot", "label", "kind", "occupied_by", "status"}))

	rec := httptest.NewRecorder()
	req := withURLParams(httptest.NewRequest(http.MethodPost, "/v1/depot/bays/"+id+"/assign",
		strings.NewReader(`{"bus_id":"22222222-2222-2222-2222-222222222222"}`)), "id", id)
	h.AssignDepotBay(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("got %d, want 409 (body: %s)", rec.Code, rec.Body)
	}
	if err := pool.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet db expectations: %v", err)
	}
}
