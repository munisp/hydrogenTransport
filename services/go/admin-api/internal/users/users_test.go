package users

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/golang-jwt/jwt/v5"
	"go.uber.org/zap"

	auth "github.com/munisp/hydrogenTransport/packages/go-auth"
	"github.com/munisp/hydrogenTransport/services/go/admin-api/internal/keycloak"
)

// Wave-10 W10-1: admin-plane lockout guards — self-disable and
// disable/demote of the last enabled platform-admin are refused.

func newTestEnv(t *testing.T) (*Handler, keycloak.AdminClient) {
	t.Helper()
	t.Setenv("H2_SIMULATED_KEYCLOAK", "true")
	kc, err := keycloak.New("http://simulated", "h2fleet", "", "", zap.NewNop())
	if err != nil {
		t.Fatalf("keycloak.New: %v", err)
	}
	return NewHandler(kc, zap.NewNop()), kc
}

func withClaims(r *http.Request, sub string) *http.Request {
	claims := jwt.MapClaims{"sub": sub}
	return r.WithContext(context.WithValue(r.Context(), auth.ClaimsKey, claims))
}

func withID(r *http.Request, id string) *http.Request {
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", id)
	return r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rctx))
}

func seedAdmin(t *testing.T, kc keycloak.AdminClient, email string) string {
	t.Helper()
	id, _, err := kc.CreateUser(context.Background(), keycloak.CreateUserSpec{
		Username: email, Email: email, DisplayName: email})
	if err != nil {
		t.Fatalf("seed user: %v", err)
	}
	if err := kc.AssignRealmRole(context.Background(), id, "platform-admin"); err != nil {
		t.Fatalf("seed role: %v", err)
	}
	return id
}

func call(h http.HandlerFunc, method, target, body, callerSub, id string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	rec := httptest.NewRecorder()
	h(rec, withID(withClaims(req, callerSub), id))
	return rec
}

func TestDisableSelfRefused(t *testing.T) {
	h, kc := newTestEnv(t)
	admin := seedAdmin(t, kc, "a@example.com")
	other := seedAdmin(t, kc, "b@example.com") // not the last admin: self-guard must be the blocker

	rec := call(h.Disable, http.MethodPost, "/v1/users/"+admin+"/disable", "", admin, admin)
	if rec.Code != http.StatusConflict {
		t.Fatalf("self-disable got %d want 409: %s", rec.Code, rec.Body.String())
	}
	// Another admin disabling them is fine.
	rec = call(h.Disable, http.MethodPost, "/v1/users/"+admin+"/disable", "", other, admin)
	if rec.Code != http.StatusOK {
		t.Fatalf("admin-on-admin disable got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestDisableLastPlatformAdminRefused(t *testing.T) {
	h, kc := newTestEnv(t)
	last := seedAdmin(t, kc, "last@example.com")
	// A non-admin caller identity (handler-level guard runs before any role
	// middleware in unit tests; the route requires platform-admin in prod).
	rec := call(h.Disable, http.MethodPost, "/v1/users/"+last+"/disable", "", "someone-else", last)
	if rec.Code != http.StatusConflict {
		t.Fatalf("disable last admin got %d want 409: %s", rec.Code, rec.Body.String())
	}

	// Once a second enabled platform-admin exists, disabling one is allowed.
	second := seedAdmin(t, kc, "second@example.com")
	rec = call(h.Disable, http.MethodPost, "/v1/users/"+last+"/disable", "", second, last)
	if rec.Code != http.StatusOK {
		t.Fatalf("disable with two admins got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestRevokeLastPlatformAdminRoleRefused(t *testing.T) {
	h, kc := newTestEnv(t)
	last := seedAdmin(t, kc, "last@example.com")

	rec := call(h.UpdateRoles, http.MethodPut, "/v1/users/"+last+"/roles",
		`{"remove":["platform-admin"]}`, "someone-else", last)
	if rec.Code != http.StatusConflict {
		t.Fatalf("demote last admin got %d want 409: %s", rec.Code, rec.Body.String())
	}

	// Non-platform-admin removals are unaffected.
	if err := kc.AssignRealmRole(context.Background(), last, "operator"); err != nil {
		t.Fatalf("assign operator: %v", err)
	}
	rec = call(h.UpdateRoles, http.MethodPut, "/v1/users/"+last+"/roles",
		`{"remove":["operator"]}`, "someone-else", last)
	if rec.Code != http.StatusOK {
		t.Fatalf("remove operator got %d: %s", rec.Code, rec.Body.String())
	}

	// With a second enabled admin, demoting one is allowed.
	seedAdmin(t, kc, "second@example.com")
	rec = call(h.UpdateRoles, http.MethodPut, "/v1/users/"+last+"/roles",
		`{"remove":["platform-admin"]}`, "someone-else", last)
	if rec.Code != http.StatusOK {
		t.Fatalf("demote with two admins got %d: %s", rec.Code, rec.Body.String())
	}
}
