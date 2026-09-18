package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/golang-jwt/jwt/v5"
	"github.com/pashagolub/pgxmock/v4"

	auth "github.com/munisp/hydrogenTransport/packages/go-auth"
)

// Wave-9 regression tests: driver registration (W9-3) and dispatch-accept
// ownership (W9-7).

func withSub(r *http.Request, sub string) *http.Request {
	claims := jwt.MapClaims{"sub": sub}
	return r.WithContext(context.WithValue(r.Context(), auth.ClaimsKey, claims))
}

func chiURLParam(r *http.Request, key, val string) *http.Request {
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add(key, val)
	return r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rctx))
}

// ---------------------------------------------------------------- W9-3 ---

func TestRegisterDriverSelfService(t *testing.T) {
	h, pool := newDispatchHandler(t)

	// Fresh registration inserts the row → 201.
	pool.ExpectQuery(`INSERT INTO infra\.drivers`).
		WithArgs("driver-1", "Dan Driver", "DL-123456").
		WillReturnRows(pgxmock.NewRows([]string{"sub"}).AddRow("driver-1"))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/drivers/register",
		strings.NewReader(`{"name":"Dan Driver","license_no":"DL-123456"}`))
	h.RegisterDriver(rec, withSub(req, "driver-1"))
	if rec.Code != http.StatusCreated {
		t.Fatalf("first register got %d: %s", rec.Code, rec.Body.String())
	}

	// Wave-10 W10-3: re-registration is INSERT-ONLY (ON CONFLICT DO NOTHING →
	// no row returned) → 200 already_registered, and crucially the stored
	// name/licence are NOT overwritten by the caller.
	pool.ExpectQuery(`INSERT INTO infra\.drivers`).
		WithArgs("driver-1", "Forged Name", "FORGED-1").
		WillReturnRows(pgxmock.NewRows([]string{"sub"}))

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/v1/drivers/register",
		strings.NewReader(`{"name":"Forged Name","license_no":"FORGED-1"}`))
	h.RegisterDriver(rec, withSub(req, "driver-1"))
	if rec.Code != http.StatusOK {
		t.Fatalf("re-register got %d want 200: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "already_registered") {
		t.Fatalf("re-register must report already_registered: %s", rec.Body.String())
	}
	if err := pool.ExpectationsWereMet(); err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
}

func TestRegisterDriverGuards(t *testing.T) {
	h, _ := newDispatchHandler(t)

	// No claims → 401 (the sub comes from the JWT, never the body).
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/drivers/register",
		strings.NewReader(`{"name":"Dan","license_no":"DL-123456"}`))
	h.RegisterDriver(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated register got %d want 401", rec.Code)
	}

	// Bad licence → 400.
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/v1/drivers/register",
		strings.NewReader(`{"name":"Dan","license_no":"AB"}`))
	h.RegisterDriver(rec, withSub(req, "driver-1"))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("short licence got %d want 400", rec.Code)
	}
}

func TestCreateDriverByOperator(t *testing.T) {
	h, pool := newDispatchHandler(t)
	pool.ExpectQuery(`INSERT INTO infra\.drivers`).
		WithArgs("kc-sub-9", "Kiosk Kate", "DL-777").
		WillReturnRows(pgxmock.NewRows([]string{"inserted"}).AddRow(true))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/drivers",
		strings.NewReader(`{"sub":"kc-sub-9","name":"Kiosk Kate","license_no":"DL-777"}`))
	h.CreateDriver(rec, withSub(req, "operator-1"))
	if rec.Code != http.StatusCreated {
		t.Fatalf("operator create got %d: %s", rec.Code, rec.Body.String())
	}
	if err := pool.ExpectationsWereMet(); err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
}

// ---------------------------------------------------------------- W9-7 ---

func jobRow(id, driverSub string) *pgxmock.Rows {
	now := time.Now()
	end := now.Add(time.Hour)
	return pgxmock.NewRows(jobCols).
		AddRow(id, driverSub, nil, "R1", &now, &end, "accepted", now, &now)
}

func TestAcceptDispatchJobOwnership(t *testing.T) {
	h, pool := newDispatchHandler(t)

	// The assignee (active) can accept: status pre-check (W10-2) then the
	// ownership-scoped UPDATE (W9-7).
	pool.ExpectQuery(`SELECT status FROM infra\.drivers`).
		WithArgs("driver-1").
		WillReturnRows(pgxmock.NewRows([]string{"status"}).AddRow("active"))
	pool.ExpectQuery(`UPDATE infra\.dispatch_jobs`).
		WithArgs("job-1", "driver-1").
		WillReturnRows(jobRow("job-1", "driver-1"))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/dispatch/jobs/job-1/accept", nil)
	h.AcceptDispatchJob(rec, chiURLParam(withSub(req, "driver-1"), "id", "job-1"))
	if rec.Code != http.StatusOK {
		t.Fatalf("assignee accept got %d: %s", rec.Code, rec.Body.String())
	}

	// A different ACTIVE driver gets 404 — the UPDATE is scoped by driver_sub
	// (no cross-driver acceptance, no existence leak).
	pool.ExpectQuery(`SELECT status FROM infra\.drivers`).
		WithArgs("driver-2").
		WillReturnRows(pgxmock.NewRows([]string{"status"}).AddRow("active"))
	pool.ExpectQuery(`UPDATE infra\.dispatch_jobs`).
		WithArgs("job-1", "driver-2").
		WillReturnRows(pgxmock.NewRows(jobCols))
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/v1/dispatch/jobs/job-1/accept", nil)
	h.AcceptDispatchJob(rec, chiURLParam(withSub(req, "driver-2"), "id", "job-1"))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("other-driver accept got %d want 404", rec.Code)
	}

	// No identity → 401, no query issued.
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/v1/dispatch/jobs/job-1/accept", nil)
	h.AcceptDispatchJob(rec, chiURLParam(req, "id", "job-1"))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated accept got %d want 401", rec.Code)
	}

	if err := pool.ExpectationsWereMet(); err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
}
