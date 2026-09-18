package handlers

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"go.uber.org/zap"
)

// --- Wave-6 A1-04 / A2-04: assignment vehicle guards --------------------------

func assignRouter(h *Handler) http.Handler {
	r := chi.NewRouter()
	r.Post("/v1/drt/requests/{id}/assign", h.AssignDRTRequest)
	return r
}

// A wheelchair ride cannot be assigned a non-accessible vehicle → 422.
func TestAssignDRTRequest_WheelchairMismatch(t *testing.T) {
	db := &flexDB{
		execFn: func(sql string, args ...any) (pgconn.CommandTag, error) {
			t.Fatalf("no UPDATE may run for an ineligible vehicle: %s", sql)
			return pgconn.CommandTag{}, nil
		},
		rowFn: func(sql string, args ...any) pgx.Row {
			if strings.Contains(sql, "WITH req AS") {
				return validationRow("wheelchair")
			}
			return noRows()
		},
	}
	h := &Handler{db: db, pub: fakePub{}, log: zap.NewNop()}

	rec := httptest.NewRecorder()
	assignRouter(h).ServeHTTP(rec, withClaims(httptest.NewRequest(http.MethodPost,
		"/v1/drt/requests/drt-w/assign", strings.NewReader(`{"vehicle_id":"veh-1"}`)), "ops-1", "operator"))

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("got %d, want 422 (body: %s)", rec.Code, rec.Body)
	}
}

// A vehicle on an overlapping active dispatch job cannot be double-booked → 409.
func TestAssignDRTRequest_VehicleBusyOnDispatch(t *testing.T) {
	db := &flexDB{
		execFn: func(sql string, args ...any) (pgconn.CommandTag, error) {
			t.Fatalf("no UPDATE may run for a dispatch-busy vehicle: %s", sql)
			return pgconn.CommandTag{}, nil
		},
		rowFn: func(sql string, args ...any) pgx.Row {
			if strings.Contains(sql, "WITH req AS") {
				return validationRow("dispatch")
			}
			return noRows()
		},
	}
	h := &Handler{db: db, pub: fakePub{}, log: zap.NewNop()}

	rec := httptest.NewRecorder()
	assignRouter(h).ServeHTTP(rec, withClaims(httptest.NewRequest(http.MethodPost,
		"/v1/drt/requests/drt-b/assign", strings.NewReader(`{"vehicle_id":"veh-2"}`)), "ops-1", "operator"))

	if rec.Code != http.StatusConflict {
		t.Fatalf("got %d, want 409 (body: %s)", rec.Code, rec.Body)
	}
}

// An unknown vehicle keeps the pre-Wave-6 422 contract.
func TestAssignDRTRequest_UnknownVehicle(t *testing.T) {
	db := &flexDB{
		rowFn: func(sql string, args ...any) pgx.Row {
			if strings.Contains(sql, "WITH req AS") {
				return validationRow("no_vehicle")
			}
			return noRows()
		},
	}
	h := &Handler{db: db, pub: fakePub{}, log: zap.NewNop()}

	rec := httptest.NewRecorder()
	assignRouter(h).ServeHTTP(rec, withClaims(httptest.NewRequest(http.MethodPost,
		"/v1/drt/requests/drt-u/assign", strings.NewReader(`{"vehicle_id":"veh-x"}`)), "ops-1", "operator"))

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("got %d, want 422 (body: %s)", rec.Code, rec.Body)
	}
}

// --- Wave-6 A3-02: erasure marker ---------------------------------------------

// The tombstone is deterministic per (salt, sub), salt-dependent (not a bare
// hash of the sub), and carries the erased: prefix — never the sub itself.
func TestErasureMarker(t *testing.T) {
	m1 := erasureMarker("user-a", "salt-1")
	m2 := erasureMarker("user-a", "salt-1")
	m3 := erasureMarker("user-a", "salt-2")
	if m1 != m2 {
		t.Fatalf("marker must be deterministic: %q vs %q", m1, m2)
	}
	if m1 == m3 {
		t.Fatalf("marker must depend on the salt")
	}
	if !strings.HasPrefix(m1, "erased:") || strings.Contains(m1, "user-a") {
		t.Fatalf("marker must be an opaque tombstone, got %q", m1)
	}
}
