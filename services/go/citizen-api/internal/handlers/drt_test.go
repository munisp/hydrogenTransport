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
	"github.com/golang-jwt/jwt/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"go.uber.org/zap"

	auth "github.com/munisp/hydrogenTransport/packages/go-auth"
)

// --------------------------------------------------------------------------
// fake DB (citizen-api has no pgxmock dependency; the handlers only need
// QueryRow for these tests)
// --------------------------------------------------------------------------

type fakeRow struct {
	scan func(dest ...any) error
}

func (r fakeRow) Scan(dest ...any) error { return r.scan(dest...) }

type fakeDB struct {
	queryRow func(sql string, args ...any) pgx.Row
}

func (f *fakeDB) Ping(context.Context) error { return nil }
func (f *fakeDB) Query(context.Context, string, ...any) (pgx.Rows, error) {
	return nil, errors.New("unexpected Query call")
}
func (f *fakeDB) QueryRow(_ context.Context, sql string, args ...any) pgx.Row {
	return f.queryRow(sql, args...)
}
func (f *fakeDB) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, errors.New("unexpected Exec call")
}
func (f *fakeDB) Begin(context.Context) (pgx.Tx, error) {
	return nil, errors.New("unexpected Begin call")
}

// selectRow fakes `SELECT status, user_sub ... WHERE id = $1`.
func selectRow(status, userSub string) pgx.Row {
	return fakeRow{scan: func(dest ...any) error {
		*(dest[0].(*string)) = status
		*(dest[1].(*string)) = userSub
		return nil
	}}
}

func noRows() pgx.Row {
	return fakeRow{scan: func(dest ...any) error { return pgx.ErrNoRows }}
}

// cancelledRow fakes `UPDATE ... RETURNING <drtCols>` for a cancelled request.
func cancelledRow(id, userSub string) pgx.Row {
	return fakeRow{scan: func(dest ...any) error {
		*(dest[0].(*string)) = id
		*(dest[1].(*string)) = userSub
		// pickup/dropoff stay nil
		*(dest[6].(*string)) = "cancelled"
		*(dest[7].(*time.Time)) = time.Date(2026, 7, 25, 10, 0, 0, 0, time.UTC)
		return nil
	}}
}

// --------------------------------------------------------------------------
// harness
// --------------------------------------------------------------------------

func withClaims(r *http.Request, sub string, roles ...string) *http.Request {
	roleList := make([]any, len(roles))
	for i, role := range roles {
		roleList[i] = role
	}
	claims := jwt.MapClaims{
		"sub":          sub,
		"realm_access": map[string]any{"roles": roleList},
	}
	return r.WithContext(context.WithValue(r.Context(), auth.ClaimsKey, claims))
}

func cancelRouter(h *Handler, sub string, roles ...string) http.Handler {
	r := chi.NewRouter()
	r.Post("/v1/drt/requests/{id}/cancel", func(w http.ResponseWriter, req *http.Request) {
		h.CancelDRTRequest(w, withClaims(req, sub, roles...))
	})
	return r
}

// --------------------------------------------------------------------------
// CancelDRTRequest ownership tests (SECURITY_AUDIT F2 / P0-2)
// --------------------------------------------------------------------------

// The rider who owns the request can cancel it.
func TestCancelDRTRequest_OwnerOK(t *testing.T) {
	db := &fakeDB{queryRow: func(sql string, args ...any) pgx.Row {
		switch {
		case contains(sql, "SELECT status, user_sub"):
			return selectRow("requested", "user-a")
		case contains(sql, "SET status = 'cancelled'"):
			if args[0] != "req-1" {
				t.Fatalf("cancel targeted wrong id: %v", args)
			}
			return cancelledRow("req-1", "user-a")
		default:
			t.Fatalf("unexpected SQL: %s", sql)
			return nil
		}
	}}
	h := &Handler{db: db, log: zap.NewNop()}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/drt/requests/req-1/cancel", nil)
	cancelRouter(h, "user-a", "citizen").ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("got %d, want 200 (body: %s)", rec.Code, rec.Body)
	}
	var d DRTRequest
	if err := json.Unmarshal(rec.Body.Bytes(), &d); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if d.Status != "cancelled" || d.UserSub != "user-a" {
		t.Fatalf("unexpected payload: %+v", d)
	}
}

// A different rider must NOT be able to cancel the request — and gets a 404
// (not 403/409) so neither existence nor status leaks.
func TestCancelDRTRequest_OtherRider404(t *testing.T) {
	updateIssued := false
	db := &fakeDB{queryRow: func(sql string, args ...any) pgx.Row {
		if contains(sql, "SET status = 'cancelled'") {
			updateIssued = true
			return cancelledRow("req-1", "user-b")
		}
		return selectRow("requested", "user-b")
	}}
	h := &Handler{db: db, log: zap.NewNop()}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/drt/requests/req-1/cancel", nil)
	cancelRouter(h, "user-a", "citizen").ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("got %d, want 404 (body: %s)", rec.Code, rec.Body)
	}
	if updateIssued {
		t.Fatal("UPDATE must not be issued for a non-owner")
	}
}

// Operators and platform-admins may cancel any rider's request.
func TestCancelDRTRequest_PrivilegedRolesAllowed(t *testing.T) {
	for _, role := range []string{"operator", "platform-admin"} {
		t.Run(role, func(t *testing.T) {
			db := &fakeDB{queryRow: func(sql string, args ...any) pgx.Row {
				if contains(sql, "SET status = 'cancelled'") {
					return cancelledRow("req-1", "user-b")
				}
				return selectRow("requested", "user-b")
			}}
			h := &Handler{db: db, log: zap.NewNop()}

			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/v1/drt/requests/req-1/cancel", nil)
			cancelRouter(h, "ops-1", role).ServeHTTP(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("%s cancel got %d, want 200 (body: %s)", role, rec.Code, rec.Body)
			}
		})
	}
}

// Unknown ids are 404 for everyone.
func TestCancelDRTRequest_NotFound(t *testing.T) {
	db := &fakeDB{queryRow: func(sql string, args ...any) pgx.Row { return noRows() }}
	h := &Handler{db: db, log: zap.NewNop()}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/drt/requests/nope/cancel", nil)
	cancelRouter(h, "user-a", "citizen").ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("got %d, want 404", rec.Code)
	}
}

func contains(s, sub string) bool { return strings.Contains(s, sub) }
