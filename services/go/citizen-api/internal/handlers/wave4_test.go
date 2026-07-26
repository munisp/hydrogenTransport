package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"go.uber.org/zap"
)

// fakePub is a no-op publisher for handler fan-out.
type fakePub struct{}

func (fakePub) Publish(context.Context, string, any) error { return nil }
func (fakePub) Close()                                     {}

// flexDB fakes QueryRow/Exec with captured args (drt wave-4 tests).
type flexDB struct {
	fakeDB
	execFn    func(sql string, args ...any) (pgconn.CommandTag, error)
	rowFn     func(sql string, args ...any) pgx.Row
	execCalls int
}

func (f *flexDB) Exec(_ context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	f.execCalls++
	if f.execFn != nil {
		return f.execFn(sql, args...)
	}
	return pgconn.NewCommandTag("UPDATE 1"), nil
}
func (f *flexDB) QueryRow(_ context.Context, sql string, args ...any) pgx.Row {
	if f.rowFn != nil {
		return f.rowFn(sql, args...)
	}
	return noRows()
}

// drtRow returns a fake Row filling the 14-column drtCols scan.
func drtRow(id, userSub, status string) pgx.Row {
	return fakeRow{scan: func(dest ...any) error {
		*(dest[0].(*string)) = id
		*(dest[1].(*string)) = userSub
		*(dest[12].(*string)) = status
		*(dest[13].(*time.Time)) = time.Date(2026, 7, 25, 10, 0, 0, 0, time.UTC)
		return nil
	}}
}

// The PWA's rider-facing fields (pickup/dropoff labels, passengers) must be
// persisted, not dropped (BUSINESS_LOGIC_AUDIT §13).
func TestCreateDRTRequest_LabelsAndPassengers(t *testing.T) {
	var gotArgs []any
	db := &flexDB{rowFn: func(sql string, args ...any) pgx.Row {
		if strings.Contains(sql, "INSERT INTO citizen.drt_requests") {
			gotArgs = args
			return drtRow("drt-1", "user-a", "requested")
		}
		return noRows()
	}}
	h := &Handler{db: db, pub: fakePub{}, log: zap.NewNop()}

	body := `{"pickup":{"lat":52.5,"lon":13.4},"dropoff":{"lat":52.51,"lon":13.41},"pickup_label":"Central Station","dropoff_label":"City Hall","passengers":3}`
	rec := httptest.NewRecorder()
	req := withClaims(httptest.NewRequest(http.MethodPost, "/v1/drt/requests", strings.NewReader(body)), "user-a")
	h.CreateDRTRequest(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("got %d, want 201 (body: %s)", rec.Code, rec.Body)
	}
	// args: sub, pickup lon/lat, dropoff lon/lat, labels, passengers
	if len(gotArgs) != 8 {
		t.Fatalf("insert args = %v, want 8 (labels + passengers persisted)", gotArgs)
	}
	if gotArgs[5] != "Central Station" || gotArgs[6] != "City Hall" || gotArgs[7] != 3 {
		t.Fatalf("labels/passengers not persisted: %v", gotArgs)
	}
}

// Manual assignment moves a requested ride to assigned with a vehicle
// (previously the assigned status was unreachable).
func TestAssignDRTRequest(t *testing.T) {
	db := &flexDB{
		execFn: func(sql string, args ...any) (pgconn.CommandTag, error) {
			if !strings.Contains(sql, "SET status = 'assigned'") {
				return pgconn.CommandTag{}, errors.New("unexpected exec: " + sql)
			}
			if args[0] != "drt-1" || args[1] != "veh-9" {
				t.Fatalf("assign args wrong: %v", args)
			}
			return pgconn.NewCommandTag("UPDATE 1"), nil
		},
		rowFn: func(sql string, args ...any) pgx.Row {
			return drtRow("drt-1", "user-a", "assigned")
		},
	}
	h := &Handler{db: db, pub: fakePub{}, log: zap.NewNop()}

	r := chi.NewRouter()
	r.Post("/v1/drt/requests/{id}/assign", h.AssignDRTRequest)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, withClaims(httptest.NewRequest(http.MethodPost,
		"/v1/drt/requests/drt-1/assign", strings.NewReader(`{"vehicle_id":"veh-9"}`)), "ops-1", "operator"))

	if rec.Code != http.StatusOK {
		t.Fatalf("got %d, want 200 (body: %s)", rec.Code, rec.Body)
	}
	var d DRTRequest
	if err := json.Unmarshal(rec.Body.Bytes(), &d); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if d.Status != "assigned" {
		t.Fatalf("unexpected request: %+v", d)
	}
	if db.execCalls != 1 {
		t.Fatalf("want 1 assignment update, got %d", db.execCalls)
	}
}

// Assigning a non-requested ride conflicts.
func TestAssignDRTRequest_NotRequested(t *testing.T) {
	db := &flexDB{execFn: func(sql string, args ...any) (pgconn.CommandTag, error) {
		return pgconn.NewCommandTag("UPDATE 0"), nil // no row in 'requested'
	}}
	h := &Handler{db: db, pub: fakePub{}, log: zap.NewNop()}

	r := chi.NewRouter()
	r.Post("/v1/drt/requests/{id}/assign", h.AssignDRTRequest)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, withClaims(httptest.NewRequest(http.MethodPost,
		"/v1/drt/requests/drt-2/assign", strings.NewReader(`{"vehicle_id":"veh-9"}`)), "ops-1", "operator"))

	if rec.Code != http.StatusConflict {
		t.Fatalf("got %d, want 409 (body: %s)", rec.Code, rec.Body)
	}
}

// The journey planner must offer one-transfer options between routes that
// share an interchange stop (BUSINESS_LOGIC_AUDIT §11: direct rides only).
func TestPlanJourney_OneTransfer(t *testing.T) {
	// 10:00 on a service day: R21 S006→S001 then R10 S001→S005.
	now := time.Date(2026, 7, 24, 10, 0, 0, 0, time.UTC)
	options, transfers := planJourney(fallbackStops, fallbackRoutes, "S006", "S005", now)
	if len(options) != 0 {
		t.Fatalf("no direct ride exists S006→S005, got %+v", options)
	}
	if len(transfers) == 0 {
		t.Fatal("expected a one-transfer option via S001")
	}
	tr := transfers[0]
	if tr.TransferStopID != "S001" || len(tr.Legs) != 2 {
		t.Fatalf("transfer must interchange at S001 with 2 legs: %+v", tr)
	}
	if tr.Legs[0].RouteID != "R21" || tr.Legs[1].RouteID != "R10" {
		t.Fatalf("legs must be R21 then R10: %+v", tr.Legs)
	}
	if !tr.Legs[1].DepartAt.After(tr.Legs[0].ArriveAt) {
		t.Fatalf("second leg must depart after the first arrives: %+v", tr.Legs)
	}
}

// Arrivals honestly mark themselves as timetable-derived (no GTFS-RT feed).
func TestGetArrivals_ScheduleBased(t *testing.T) {
	h := &Handler{}
	rec := httptest.NewRecorder()
	h.GetArrivals(rec, httptest.NewRequest(http.MethodGet, "/v1/passenger/arrivals?stop_id=S001", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d (body: %s)", rec.Code, rec.Body)
	}
	var body struct {
		Arrivals []Arrival `json:"arrivals"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	for _, a := range body.Arrivals {
		if !a.ScheduleBased {
			t.Fatalf("arrival must be flagged schedule_based: %+v", a)
		}
	}
}

// GTFS static feed: per-file CSV and the zip bundle.
func TestGTFSFile_Stops(t *testing.T) {
	h := &Handler{}
	r := chi.NewRouter()
	r.Get("/v1/opendata/gtfs/{file}", h.GTFSFile)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/opendata/gtfs/stops.txt", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("got %d (body: %s)", rec.Code, rec.Body)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "text/csv") {
		t.Fatalf("content-type = %q, want text/csv", ct)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "stop_id,stop_name,stop_lat,stop_lon") || !strings.Contains(body, "S001") {
		t.Fatalf("stops.txt malformed:\n%s", body)
	}
}

func TestGTFSZip_Bundle(t *testing.T) {
	h := &Handler{}
	rec := httptest.NewRecorder()
	h.GTFSZip(rec, httptest.NewRequest(http.MethodGet, "/v1/opendata/gtfs", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("got %d (body: %s)", rec.Code, rec.Body)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/zip" {
		t.Fatalf("content-type = %q, want application/zip", ct)
	}
	// PK zip magic
	if rec.Body.Len() < 4 || rec.Body.Bytes()[0] != 'P' || rec.Body.Bytes()[1] != 'K' {
		t.Fatal("response is not a zip archive")
	}
}
