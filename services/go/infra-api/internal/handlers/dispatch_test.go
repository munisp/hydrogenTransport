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

var jobCols = []string{"id", "driver_sub", "vehicle_id", "route", "starts_at", "status", "created_at", "accepted_at"}

// A driver already on an active (assigned/accepted/in_progress) job must be
// rejected with 409 (BUSINESS_LOGIC_AUDIT §8: no double-booking).
func TestCreateDispatchJob_DriverConflict(t *testing.T) {
	h, pool := newDispatchHandler(t)

	pool.ExpectBegin()
	pool.ExpectQuery(conflictQuery).
		WithArgs("driver-1", (*string)(nil)).
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

// A vehicle already bound to an active job must be rejected with 409.
func TestCreateDispatchJob_VehicleConflict(t *testing.T) {
	h, pool := newDispatchHandler(t)

	vehicle := "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"
	pool.ExpectBegin()
	pool.ExpectQuery(conflictQuery).
		WithArgs("driver-2", &vehicle).
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

// No conflict → the insert commits and the job is returned.
func TestCreateDispatchJob_NoConflict(t *testing.T) {
	h, pool := newDispatchHandler(t)

	vehicle := "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"
	created := time.Date(2026, 7, 25, 9, 0, 0, 0, time.UTC)
	pool.ExpectBegin()
	pool.ExpectQuery(conflictQuery).
		WithArgs("driver-3", &vehicle).
		WillReturnRows(pgxmock.NewRows([]string{"conflict"})) // no active job
	pool.ExpectQuery(`INSERT INTO infra\.dispatch_jobs`).
		WithArgs("driver-3", &vehicle, "R10", (*time.Time)(nil)).
		WillReturnRows(pgxmock.NewRows(jobCols).
			AddRow("cccccccc-cccc-cccc-cccc-cccccccccccc", "driver-3", &vehicle, "R10", nil, "assigned", created, nil))
	pool.ExpectCommit()

	rec := createJob(t, h, `{"driver_sub":"driver-3","vehicle_id":"`+vehicle+`","route":"R10"}`)
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
	if err := pool.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet db expectations: %v", err)
	}
}
