package handlers

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/pashagolub/pgxmock/v4"
)

// Wave-10 regression tests: driver suspension enforcement (W10-2) and the
// driver status lifecycle endpoint.

func TestAcceptDispatchJobSuspendedDriver(t *testing.T) {
	h, pool := newDispatchHandler(t)

	// Suspended driver → 403, no UPDATE issued (JWT/role outlive suspension;
	// the drivers table is the operational source of truth).
	pool.ExpectQuery(`SELECT status FROM infra\.drivers`).
		WithArgs("driver-9").
		WillReturnRows(pgxmock.NewRows([]string{"status"}).AddRow("suspended"))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/dispatch/jobs/job-1/accept", nil)
	h.AcceptDispatchJob(rec, chiURLParam(withSub(req, "driver-9"), "id", "job-1"))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("suspended accept got %d want 403: %s", rec.Code, rec.Body.String())
	}

	// Unregistered driver (no drivers row at all) → 403 pointing at
	// registration.
	pool.ExpectQuery(`SELECT status FROM infra\.drivers`).
		WithArgs("ghost").
		WillReturnRows(pgxmock.NewRows([]string{"status"}))
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/v1/dispatch/jobs/job-1/accept", nil)
	h.AcceptDispatchJob(rec, chiURLParam(withSub(req, "ghost"), "id", "job-1"))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("unregistered accept got %d want 403: %s", rec.Code, rec.Body.String())
	}

	if err := pool.ExpectationsWereMet(); err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
}

func TestSetDriverStatus(t *testing.T) {
	h, pool := newDispatchHandler(t)

	// Suspend an existing driver.
	pool.ExpectExec(`UPDATE infra\.drivers SET status`).
		WithArgs("driver-1", "suspended").
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/drivers/driver-1/status",
		strings.NewReader(`{"status":"suspended"}`))
	h.SetDriverStatus(rec, chiURLParam(withSub(req, "operator-1"), "sub", "driver-1"))
	if rec.Code != http.StatusOK {
		t.Fatalf("suspend got %d: %s", rec.Code, rec.Body.String())
	}

	// Unknown driver → 404.
	pool.ExpectExec(`UPDATE infra\.drivers SET status`).
		WithArgs("nope", "active").
		WillReturnResult(pgxmock.NewResult("UPDATE", 0))
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/v1/drivers/nope/status",
		strings.NewReader(`{"status":"active"}`))
	h.SetDriverStatus(rec, chiURLParam(withSub(req, "operator-1"), "sub", "nope"))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown driver got %d want 404", rec.Code)
	}

	// Invalid status value → 400 (no query issued).
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/v1/drivers/driver-1/status",
		strings.NewReader(`{"status":"banished"}`))
	h.SetDriverStatus(rec, chiURLParam(withSub(req, "operator-1"), "sub", "driver-1"))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid status got %d want 400", rec.Code)
	}

	if err := pool.ExpectationsWereMet(); err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
}
