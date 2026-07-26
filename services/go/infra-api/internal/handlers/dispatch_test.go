package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/pashagolub/pgxmock/v4"
	"go.uber.org/zap"
)

// testPublisher / testSignaler are no-op fakes so CreateDispatchJob can run
// its post-commit fan-out without Kafka/Temporal.
type testPublisher struct{}

func (testPublisher) Publish(context.Context, string, any) error { return nil }
func (testPublisher) Close()                                     {}

type testSignaler struct{}

func (testSignaler) Signal(context.Context, string, string, any) error { return nil }
func (testSignaler) Close()                                            {}

func newDispatchHandler(t *testing.T) (*Handler, pgxmock.PgxPoolIface) {
	t.Helper()
	pool, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock.NewPool: %v", err)
	}
	t.Cleanup(pool.Close)
	return &Handler{db: pool, pub: testPublisher{}, wf: testSignaler{}, log: zap.NewNop()}, pool
}

func createJob(t *testing.T, h *Handler, body string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/dispatch/jobs", strings.NewReader(body))
	h.CreateDispatchJob(rec, req)
	return rec
}

const conflictQuery = `FROM infra\.dispatch_jobs`

var jobCols = []string{"id", "driver_sub", "vehicle_id", "route", "starts_at", "ends_at", "status", "created_at", "accepted_at"}

// A driver already on an overlapping active (assigned/accepted/in_progress)
// job must be rejected with 409 (BUSINESS_LOGIC_AUDIT §8: no double-booking).
func TestCreateDispatchJob_DriverConflict(t *testing.T) {
	h, pool := newDispatchHandler(t)

	pool.ExpectBegin()
	pool.ExpectQuery(conflictQuery).
		WithArgs("driver-1", (*string)(nil), (*time.Time)(nil), (*time.Time)(nil)).
		WillReturnRows(pgxmock.NewRows([]string{"conflict"}).AddRow("driver"))
	pool.ExpectRollback()

	rec := createJob(t, h, `{"driver_sub":"driver-1","route":"R10"}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("got %d, want 409 (body: %s)", rec.Code, rec.Body)
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !strings.Contains(body["error"], "driver") {
		t.Fatalf("conflict message should name the driver: %v", body)
	}
	if err := pool.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet db expectations: %v", err)
	}
}

// A vehicle already bound to an overlapping active job must be rejected with 409.
func TestCreateDispatchJob_VehicleConflict(t *testing.T) {
	h, pool := newDispatchHandler(t)

	vehicle := "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"
	pool.ExpectBegin()
	pool.ExpectQuery(conflictQuery).
		WithArgs("driver-2", &vehicle, (*time.Time)(nil), (*time.Time)(nil)).
		WillReturnRows(pgxmock.NewRows([]string{"conflict"}).AddRow("vehicle"))
	pool.ExpectRollback()

	rec := createJob(t, h, `{"driver_sub":"driver-2","vehicle_id":"`+vehicle+`","route":"R10"}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("got %d, want 409 (body: %s)", rec.Code, rec.Body)
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !strings.Contains(body["error"], "vehicle") {
		t.Fatalf("conflict message should name the vehicle: %v", body)
	}
	if err := pool.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet db expectations: %v", err)
	}
}

// ends_at before starts_at is rejected client-side (no DB call).
func TestCreateDispatchJob_EndsBeforeStarts(t *testing.T) {
	h, pool := newDispatchHandler(t)

	rec := createJob(t, h, `{"driver_sub":"driver-1","starts_at":"2026-07-25T10:00:00Z","ends_at":"2026-07-25T09:00:00Z"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("got %d, want 400 (body: %s)", rec.Code, rec.Body)
	}
	if err := pool.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet db expectations: %v", err)
	}
}

// No conflict → the insert commits (with ends_at → shift_end) and the job is returned.
func TestCreateDispatchJob_NoConflict(t *testing.T) {
	h, pool := newDispatchHandler(t)

	vehicle := "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"
	created := time.Date(2026, 7, 25, 9, 0, 0, 0, time.UTC)
	starts := time.Date(2026, 7, 26, 8, 0, 0, 0, time.UTC)
	ends := time.Date(2026, 7, 26, 16, 0, 0, 0, time.UTC)
	pool.ExpectBegin()
	pool.ExpectQuery(conflictQuery).
		WithArgs("driver-3", &vehicle, &starts, &ends).
		WillReturnRows(pgxmock.NewRows([]string{"conflict"})) // no overlapping active job
	pool.ExpectQuery(`INSERT INTO infra\.dispatch_jobs`).
		WithArgs("driver-3", &vehicle, "R10", &starts, &ends).
		WillReturnRows(pgxmock.NewRows(jobCols).
			AddRow("cccccccc-cccc-cccc-cccc-cccccccccccc", "driver-3", &vehicle, "R10", &starts, &ends, "assigned", created, nil))
	pool.ExpectCommit()

	rec := createJob(t, h, `{"driver_sub":"driver-3","vehicle_id":"`+vehicle+`","route":"R10","starts_at":"2026-07-26T08:00:00Z","ends_at":"2026-07-26T16:00:00Z"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("got %d, want 201 (body: %s)", rec.Code, rec.Body)
	}
	var j DispatchJob
	if err := json.Unmarshal(rec.Body.Bytes(), &j); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if j.Status != "assigned" || j.DriverSub != "driver-3" {
		t.Fatalf("unexpected job payload: %+v", j)
	}
	if j.EndsAt == nil || !j.EndsAt.Equal(ends) {
		t.Fatalf("ends_at (shift_end source) not echoed: %+v", j.EndsAt)
	}
	if err := pool.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet db expectations: %v", err)
	}
}

// GET /v1/dispatch/jobs?driver_sub= must push the filter into SQL (the
// mobile DriverScreen depends on it; previously silently ignored).
func TestListDispatchJobs_DriverFilter(t *testing.T) {
	h, pool := newDispatchHandler(t)

	pool.ExpectQuery(`driver_sub = \$2`).
		WithArgs("assigned", "driver-9").
		WillReturnRows(pgxmock.NewRows(jobCols))

	rec := httptest.NewRecorder()
	h.ListDispatchJobs(rec, httptest.NewRequest(http.MethodGet, "/v1/dispatch/jobs?status=assigned&driver_sub=driver-9", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d, want 200 (body: %s)", rec.Code, rec.Body)
	}
	if err := pool.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet db expectations: %v", err)
	}
}

// POST /v1/dispatch/jobs/{id}/cancel cancels an active job (delivers the
// workflow's job-cancelled signal).
func TestCancelDispatchJob(t *testing.T) {
	h, pool := newDispatchHandler(t)

	created := time.Date(2026, 7, 25, 9, 0, 0, 0, time.UTC)
	pool.ExpectQuery(`UPDATE infra\.dispatch_jobs`).
		WithArgs("cccccccc-cccc-cccc-cccc-cccccccccccc").
		WillReturnRows(pgxmock.NewRows(jobCols).
			AddRow("cccccccc-cccc-cccc-cccc-cccccccccccc", "driver-3", nil, "R10", nil, nil, "cancelled", created, nil))

	rec := httptest.NewRecorder()
	req := withURLParams(httptest.NewRequest(http.MethodPost,
		"/v1/dispatch/jobs/cccccccc-cccc-cccc-cccc-cccccccccccc/cancel", nil),
		"id", "cccccccc-cccc-cccc-cccc-cccccccccccc")
	h.CancelDispatchJob(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d, want 200 (body: %s)", rec.Code, rec.Body)
	}
	if err := pool.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet db expectations: %v", err)
	}
}

// Cancelling a completed/already-cancelled job conflicts.
func TestCancelDispatchJob_NotActive(t *testing.T) {
	h, pool := newDispatchHandler(t)

	pool.ExpectQuery(`UPDATE infra\.dispatch_jobs`).
		WithArgs("cccccccc-cccc-cccc-cccc-cccccccccccc").
		WillReturnRows(pgxmock.NewRows(jobCols)) // no row → not active

	rec := httptest.NewRecorder()
	req := withURLParams(httptest.NewRequest(http.MethodPost,
		"/v1/dispatch/jobs/cccccccc-cccc-cccc-cccc-cccccccccccc/cancel", nil),
		"id", "cccccccc-cccc-cccc-cccc-cccccccccccc")
	h.CancelDispatchJob(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("got %d, want 409 (body: %s)", rec.Code, rec.Body)
	}
	if err := pool.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet db expectations: %v", err)
	}
}
