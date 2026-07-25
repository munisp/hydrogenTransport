package onboarding

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/golang-jwt/jwt/v5"
	"go.uber.org/zap"

	auth "github.com/munisp/hydrogenTransport/packages/go-auth"
	"github.com/munisp/hydrogenTransport/services/go/admin-api/internal/keycloak"
)

// --------------------------------------------------------------------------
// fakes
// --------------------------------------------------------------------------

type fakeStore struct {
	mu   sync.Mutex
	byID map[string]*Request
	seq  int
}

func newFakeStore() *fakeStore { return &fakeStore{byID: map[string]*Request{}} }

func (s *fakeStore) EnsureSchema(context.Context) error { return nil }
func (s *fakeStore) Ping(context.Context) error         { return nil }

func (s *fakeStore) Create(_ context.Context, req *Request) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seq++
	cp := *req
	cp.ID = fmt.Sprintf("req-%d", s.seq)
	cp.CreatedAt = time.Now().UTC()
	if len(cp.Meta) == 0 {
		cp.Meta = json.RawMessage(`{}`)
	}
	s.byID[cp.ID] = &cp
	*req = cp
	return nil
}

func (s *fakeStore) Get(_ context.Context, id string) (*Request, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.byID[id]
	if !ok {
		return nil, ErrNotFound
	}
	cp := *r
	return &cp, nil
}

func (s *fakeStore) List(_ context.Context, status, persona string, _ int) ([]Request, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []Request{}
	for _, r := range s.byID {
		if status != "" && r.Status != status {
			continue
		}
		if persona != "" && r.Persona != persona {
			continue
		}
		out = append(out, *r)
	}
	return out, nil
}

func (s *fakeStore) Decide(_ context.Context, id, status, kcSub, decidedBy, reason string) (*Request, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.byID[id]
	if !ok {
		return nil, ErrNotFound
	}
	r.Status = status
	if kcSub != "" {
		r.KeycloakSub = kcSub
	}
	now := time.Now().UTC()
	r.DecidedAt = &now
	r.DecidedBy = decidedBy
	if reason != "" {
		r.Meta = json.RawMessage(`{"reject_reason":` + strconv(reason) + `}`)
	}
	cp := *r
	return &cp, nil
}

func strconv(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

type fakeKC struct {
	mu            sync.Mutex
	created       []keycloak.CreateUserSpec
	assignedRoles map[string][]string
	actionsSent   map[string][]string
	passwords     map[string]string
	failCreate    bool
}

func newFakeKC() *fakeKC {
	return &fakeKC{
		assignedRoles: map[string][]string{},
		actionsSent:   map[string][]string{},
		passwords:     map[string]string{},
	}
}

func (f *fakeKC) CreateUser(_ context.Context, spec keycloak.CreateUserSpec) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failCreate {
		return "", errors.New("keycloak down")
	}
	f.created = append(f.created, spec)
	return fmt.Sprintf("kc-%d", len(f.created)), nil
}

func (f *fakeKC) SetTemporaryPassword(_ context.Context, userID, password string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.passwords[userID] = password
	return nil
}

func (f *fakeKC) AssignRealmRole(_ context.Context, userID, role string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.assignedRoles[userID] = append(f.assignedRoles[userID], role)
	return nil
}

func (f *fakeKC) SendActionsEmail(_ context.Context, userID string, actions []string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.actionsSent[userID] = append([]string{}, actions...)
	return nil
}

// --------------------------------------------------------------------------
// harness
// --------------------------------------------------------------------------

// injectClaims mimics the JWT middleware: it places validated claims for the
// given subject/roles into the request context so role checks in handlers
// can be exercised without a JWKS round-trip.
func injectClaims(sub string, roles ...string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			roleList := make([]any, len(roles))
			for i, role := range roles {
				roleList[i] = role
			}
			claims := jwt.MapClaims{"sub": sub, "realm_access": map[string]any{"roles": roleList}}
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), auth.ClaimsKey, claims)))
		})
	}
}

func newTestRouter(h *Handler, sub string, roles ...string) *chi.Mux {
	r := chi.NewRouter()
	r.Use(injectClaims(sub, roles...))
	r.Post("/v1/onboarding/citizen", h.CitizenSelfServe)
	r.Post("/v1/onboarding/{key}", h.Intake)
	r.Get("/v1/onboarding", h.List)
	r.Get("/v1/onboarding/{key}", h.Get)
	r.Post("/v1/onboarding/{key}/approve", h.Approve)
	r.Post("/v1/onboarding/{key}/reject", h.Reject)
	return r
}

// newTestHandler returns a router whose caller carries the platform-admin
// role (approve/reject are platform-admin only — SECURITY_AUDIT F3).
func newTestHandler() (*Handler, *fakeStore, *fakeKC, *chi.Mux) {
	store := newFakeStore()
	kc := newFakeKC()
	h := NewHandler(store, kc, zap.NewNop(), func() string { return "TmpPassw0rd!" })
	return h, store, kc, newTestRouter(h, "admin-1", "platform-admin")
}

func do(t *testing.T, router http.Handler, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	var rdr *strings.Reader
	if body == "" {
		rdr = strings.NewReader("")
	} else {
		rdr = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, rdr)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

func decodeBody(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatalf("response is not JSON: %v (%q)", err, rec.Body.String())
	}
	return m
}

// --------------------------------------------------------------------------
// tests
// --------------------------------------------------------------------------

func TestIntakeValidation(t *testing.T) {
	_, _, _, router := newTestHandler()

	cases := []struct {
		name       string
		persona    string
		body       string
		wantStatus int
	}{
		{"bad email", "driver", `{"email":"not-an-email","display_name":"Jane"}`, http.StatusBadRequest},
		{"missing display_name", "driver", `{"email":"j@example.com"}`, http.StatusBadRequest},
		{"unknown persona", "astronaut", `{"email":"j@example.com","display_name":"Jane"}`, http.StatusNotFound},
		// NB: "citizen" resolves to the static self-serve route (chi prefers
		// static segments over wildcards), so it yields 201, not 400.
		{"invalid meta", "operator", `{"email":"j@example.com","display_name":"Jane","meta":notjson}`, http.StatusBadRequest},
		{"valid driver", "driver", `{"email":"j@example.com","display_name":"Jane","org":"City Transit"}`, http.StatusCreated},
		{"valid gov-viewer", "gov-viewer", `{"email":"g@example.com","display_name":"Gov"}`, http.StatusCreated},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := do(t, router, http.MethodPost, "/v1/onboarding/"+tc.persona, tc.body)
			if rec.Code != tc.wantStatus {
				t.Fatalf("got %d want %d body=%s", rec.Code, tc.wantStatus, rec.Body.String())
			}
			if tc.wantStatus >= 400 {
				if m := decodeBody(t, rec); m["error"] == nil {
					t.Fatalf("error envelope missing: %s", rec.Body.String())
				}
			}
		})
	}
}

func TestIntakeCreatesPendingRequest(t *testing.T) {
	_, _, kc, router := newTestHandler()
	rec := do(t, router, http.MethodPost, "/v1/onboarding/operator",
		`{"email":"op@example.com","display_name":"Olivia Operator","org":"H2 Ops"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("got %d: %s", rec.Code, rec.Body.String())
	}
	m := decodeBody(t, rec)
	reqObj := m["request"].(map[string]any)
	if reqObj["status"] != StatusPending {
		t.Fatalf("intake must create status=pending, got %v", reqObj["status"])
	}
	if reqObj["persona"] != "operator" || reqObj["id"] == "" {
		t.Fatalf("unexpected request payload: %v", reqObj)
	}
	// Intake alone must NOT provision a Keycloak user.
	if len(kc.created) != 0 {
		t.Fatalf("no keycloak user should be provisioned at intake")
	}
}

func TestCitizenSelfServeProvisionsImmediately(t *testing.T) {
	_, _, kc, router := newTestHandler()
	rec := do(t, router, http.MethodPost, "/v1/onboarding/citizen",
		`{"email":"c@example.com","display_name":"Cora Citizen"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("got %d: %s", rec.Code, rec.Body.String())
	}
	m := decodeBody(t, rec)
	reqObj := m["request"].(map[string]any)
	if reqObj["status"] != StatusCompleted {
		t.Fatalf("citizen self-serve must complete immediately, got %v", reqObj["status"])
	}
	if reqObj["keycloak_sub"] != "kc-1" {
		t.Fatalf("keycloak_sub not recorded: %v", reqObj)
	}
	// Provisioned with the citizen role, temp password and actions email.
	if got := kc.assignedRoles["kc-1"]; len(got) != 1 || got[0] != "citizen" {
		t.Fatalf("expected citizen role assignment, got %v", got)
	}
	if kc.passwords["kc-1"] != "TmpPassw0rd!" {
		t.Fatalf("temporary password not set")
	}
	if got := kc.actionsSent["kc-1"]; len(got) != 2 || got[0] != "VERIFY_EMAIL" || got[1] != "UPDATE_PASSWORD" {
		t.Fatalf("expected VERIFY_EMAIL+UPDATE_PASSWORD actions email, got %v", got)
	}
}

func TestCitizenSelfServeKeycloakFailure(t *testing.T) {
	_, _, kc, router := newTestHandler()
	kc.failCreate = true
	rec := do(t, router, http.MethodPost, "/v1/onboarding/citizen",
		`{"email":"c@example.com","display_name":"Cora Citizen"}`)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("got %d want 502: %s", rec.Code, rec.Body.String())
	}
}

func TestApproveFlow(t *testing.T) {
	_, store, kc, router := newTestHandler()
	// Seed a pending driver request via intake.
	rec := do(t, router, http.MethodPost, "/v1/onboarding/driver",
		`{"email":"d@example.com","display_name":"Dan Driver","org":"Depot"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("intake failed: %d %s", rec.Code, rec.Body.String())
	}
	id := decodeBody(t, rec)["request"].(map[string]any)["id"].(string)

	// Approve -> completed, Keycloak user with driver role.
	rec = do(t, router, http.MethodPost, "/v1/onboarding/"+id+"/approve", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("approve got %d: %s", rec.Code, rec.Body.String())
	}
	reqObj := decodeBody(t, rec)["request"].(map[string]any)
	if reqObj["status"] != StatusCompleted {
		t.Fatalf("approve must complete the request, got %v", reqObj["status"])
	}
	if reqObj["keycloak_sub"] != "kc-1" {
		t.Fatalf("keycloak_sub missing after approve: %v", reqObj)
	}
	if got := kc.assignedRoles["kc-1"]; len(got) != 1 || got[0] != "driver" {
		t.Fatalf("driver persona must map to driver realm role, got %v", got)
	}
	if len(kc.actionsSent["kc-1"]) != 2 {
		t.Fatalf("actions email not sent on approve")
	}

	// Approving a decided request conflicts.
	rec = do(t, router, http.MethodPost, "/v1/onboarding/"+id+"/approve", "")
	if rec.Code != http.StatusConflict {
		t.Fatalf("re-approve got %d want 409", rec.Code)
	}

	// The stored row reflects the decision.
	stored, err := store.Get(context.Background(), id)
	if err != nil || stored.Status != StatusCompleted {
		t.Fatalf("store not updated: %v %+v", err, stored)
	}
}

func TestApprovePersonaRoleMapping(t *testing.T) {
	want := map[string]string{
		"driver": "driver", "operator": "operator", "station-staff": "operator",
		"advertiser": "citizen", "data-partner": "citizen", "gov-viewer": "citizen",
	}
	for persona, role := range want {
		t.Run(persona, func(t *testing.T) {
			if got := RealmRole(persona); got != role {
				t.Fatalf("RealmRole(%q) = %q want %q", persona, got, role)
			}
		})
	}
}

func TestApproveProvisionsMappedRole(t *testing.T) {
	_, _, kc, router := newTestHandler()
	rec := do(t, router, http.MethodPost, "/v1/onboarding/station-staff",
		`{"email":"s@example.com","display_name":"Sue Staff"}`)
	id := decodeBody(t, rec)["request"].(map[string]any)["id"].(string)
	if rec = do(t, router, http.MethodPost, "/v1/onboarding/"+id+"/approve", ""); rec.Code != http.StatusOK {
		t.Fatalf("approve got %d: %s", rec.Code, rec.Body.String())
	}
	if got := kc.assignedRoles["kc-1"]; len(got) != 1 || got[0] != "operator" {
		t.Fatalf("station-staff must map to operator realm role, got %v", got)
	}
}

// Operators (and any non-platform-admin) may list/view onboarding requests
// but must NOT be able to approve or reject them — approving an operator or
// station-staff intake would let one operator mint further operator accounts
// (SECURITY_AUDIT F3, privilege self-replication).
func TestOperatorCannotDecide(t *testing.T) {
	store := newFakeStore()
	kc := newFakeKC()
	h := NewHandler(store, kc, zap.NewNop(), func() string { return "TmpPassw0rd!" })
	adminRouter := newTestRouter(h, "admin-1", "platform-admin")
	operatorRouter := newTestRouter(h, "op-1", "operator")

	// Seed a pending operator intake (the most dangerous persona).
	rec := do(t, adminRouter, http.MethodPost, "/v1/onboarding/operator",
		`{"email":"o@example.com","display_name":"Otto Operator"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("intake failed: %d %s", rec.Code, rec.Body.String())
	}
	id := decodeBody(t, rec)["request"].(map[string]any)["id"].(string)

	// Operator can still VIEW the queue.
	if rec = do(t, operatorRouter, http.MethodGet, "/v1/onboarding", ""); rec.Code != http.StatusOK {
		t.Fatalf("operator list got %d want 200", rec.Code)
	}

	// ...but approving is forbidden and must not provision anything.
	rec = do(t, operatorRouter, http.MethodPost, "/v1/onboarding/"+id+"/approve", "")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("operator approve got %d want 403 (body: %s)", rec.Code, rec.Body)
	}
	if len(kc.created) != 0 {
		t.Fatalf("forbidden approve must not provision a keycloak user")
	}

	// Rejecting is likewise forbidden.
	rec = do(t, operatorRouter, http.MethodPost, "/v1/onboarding/"+id+"/reject", `{"reason":"x"}`)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("operator reject got %d want 403 (body: %s)", rec.Code, rec.Body)
	}

	// The request is untouched (still pending).
	stored, err := store.Get(context.Background(), id)
	if err != nil || stored.Status != StatusPending {
		t.Fatalf("request must remain pending after forbidden decisions: %v %+v", err, stored)
	}

	// platform-admin CAN approve, proving the gate is role-specific.
	rec = do(t, adminRouter, http.MethodPost, "/v1/onboarding/"+id+"/approve", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("platform-admin approve got %d want 200 (body: %s)", rec.Code, rec.Body)
	}
	if got := kc.assignedRoles["kc-1"]; len(got) != 1 || got[0] != "operator" {
		t.Fatalf("operator persona must map to operator realm role, got %v", got)
	}
}

func TestRejectFlow(t *testing.T) {
	_, _, kc, router := newTestHandler()
	rec := do(t, router, http.MethodPost, "/v1/onboarding/advertiser",
		`{"email":"a@example.com","display_name":"Ad Annie"}`)
	id := decodeBody(t, rec)["request"].(map[string]any)["id"].(string)

	rec = do(t, router, http.MethodPost, "/v1/onboarding/"+id+"/reject", `{"reason":"duplicate account"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("reject got %d: %s", rec.Code, rec.Body.String())
	}
	reqObj := decodeBody(t, rec)["request"].(map[string]any)
	if reqObj["status"] != StatusRejected {
		t.Fatalf("reject must set status=rejected, got %v", reqObj["status"])
	}
	meta := reqObj["meta"].(map[string]any)
	if meta["reject_reason"] != "duplicate account" {
		t.Fatalf("reject reason not stored in meta: %v", meta)
	}
	if len(kc.created) != 0 {
		t.Fatalf("rejected request must not provision a keycloak user")
	}

	// Rejecting a decided request conflicts; unknown id 404s.
	if rec = do(t, router, http.MethodPost, "/v1/onboarding/"+id+"/reject", ""); rec.Code != http.StatusConflict {
		t.Fatalf("re-reject got %d want 409", rec.Code)
	}
	if rec = do(t, router, http.MethodPost, "/v1/onboarding/nope/approve", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("approve unknown id got %d want 404", rec.Code)
	}
}

func TestListFilters(t *testing.T) {
	_, _, _, router := newTestHandler()
	do(t, router, http.MethodPost, "/v1/onboarding/driver", `{"email":"d@example.com","display_name":"D"}`)
	do(t, router, http.MethodPost, "/v1/onboarding/operator", `{"email":"o@example.com","display_name":"O"}`)
	do(t, router, http.MethodPost, "/v1/onboarding/citizen", `{"email":"c@example.com","display_name":"C"}`)

	rec := do(t, router, http.MethodGet, "/v1/onboarding?status=pending", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("list got %d", rec.Code)
	}
	reqs := decodeBody(t, rec)["requests"].([]any)
	if len(reqs) != 2 { // driver+operator pending; citizen completed
		t.Fatalf("expected 2 pending requests, got %d", len(reqs))
	}

	rec = do(t, router, http.MethodGet, "/v1/onboarding?persona=driver", "")
	reqs = decodeBody(t, rec)["requests"].([]any)
	if len(reqs) != 1 || reqs[0].(map[string]any)["persona"] != "driver" {
		t.Fatalf("persona filter broken: %v", reqs)
	}

	if rec = do(t, router, http.MethodGet, "/v1/onboarding?status=bogus", ""); rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid status filter got %d want 400", rec.Code)
	}
}
