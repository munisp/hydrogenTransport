package onboarding

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
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

func (s *fakeStore) List(_ context.Context, status, persona string, limit, offset int) ([]Request, error) {
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
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].ID > out[j].ID
		}
		return out[i].CreatedAt.After(out[j].CreatedAt)
	})
	if offset > len(out) {
		return []Request{}, nil
	}
	out = out[offset:]
	if limit > 0 && len(out) > limit {
		out = out[:limit]
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
		r.Meta = json.RawMessage(`{"reject_reason":` + quote(reason) + `}`)
	}
	cp := *r
	return &cp, nil
}

func (s *fakeStore) FindPending(_ context.Context, persona, email string) (*Request, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range s.byID {
		if r.Persona == persona && strings.EqualFold(r.Email, email) && r.Status == StatusPending {
			cp := *r
			return &cp, nil
		}
	}
	return nil, ErrNotFound
}

func (s *fakeStore) CountRecent(_ context.Context, email string, since time.Time) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, r := range s.byID {
		if strings.EqualFold(r.Email, email) && !r.CreatedAt.Before(since) {
			n++
		}
	}
	return n, nil
}

func (s *fakeStore) ExpirePending(_ context.Context, before time.Time) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var n int64
	for _, r := range s.byID {
		if r.Status == StatusPending && r.CreatedAt.Before(before) {
			r.Status = StatusExpired
			n++
		}
	}
	return n, nil
}

func (s *fakeStore) MergeMeta(_ context.Context, id string, patch map[string]any) (*Request, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.byID[id]
	if !ok {
		return nil, ErrNotFound
	}
	meta := map[string]any{}
	if len(r.Meta) > 0 {
		_ = json.Unmarshal(r.Meta, &meta)
	}
	for k, v := range patch {
		meta[k] = v
	}
	r.Meta, _ = json.Marshal(meta)
	cp := *r
	return &cp, nil
}

// seed inserts a request row directly (tests that need a controlled
// created_at, e.g. the pending-TTL expiry guard).
func (s *fakeStore) seed(req *Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := *req
	if len(cp.Meta) == 0 {
		cp.Meta = json.RawMessage(`{}`)
	}
	s.byID[cp.ID] = &cp
}

func quote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

type fakeKC struct {
	mu            sync.Mutex
	created       []keycloak.CreateUserSpec
	existing      map[string]string // email → user id: pre-registered accounts (W9-1 tests)
	assignedRoles map[string][]string
	ensureCalls   int
	actionsSent   map[string][]string
	passwords     map[string]string
	failCreate    bool
	failEnsure    bool
}

func newFakeKC() *fakeKC {
	return &fakeKC{
		existing:      map[string]string{},
		assignedRoles: map[string][]string{},
		actionsSent:   map[string][]string{},
		passwords:     map[string]string{},
	}
}

// seedExisting pre-registers an account (as if a previous onboarding or the
// user directory already created it) and returns its Keycloak id.
func (f *fakeKC) seedExisting(email, id string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.existing[strings.ToLower(email)] = id
}

func (f *fakeKC) CreateUser(_ context.Context, spec keycloak.CreateUserSpec) (string, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failCreate {
		return "", false, errors.New("keycloak down")
	}
	if id, ok := f.existing[strings.ToLower(spec.Email)]; ok {
		return id, true, nil // 409-conflict adopt: caller MUST NOT reset credentials
	}
	f.created = append(f.created, spec)
	return fmt.Sprintf("kc-%d", len(f.created)), false, nil
}

func (f *fakeKC) SetTemporaryPassword(_ context.Context, userID, password string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.passwords[userID] = password
	return nil
}

func (f *fakeKC) EnsureRealmRole(_ context.Context, userID, role string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ensureCalls++
	if f.failEnsure {
		return errors.New("role-mappings read failed")
	}
	for _, r := range f.assignedRoles[userID] {
		if r == role {
			return nil // already correct: idempotent no-op
		}
	}
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
	r.Get("/v1/onboarding/status/{id}", h.StatusPublic)
	r.Get("/v1/onboarding", h.List)
	r.Get("/v1/onboarding/{key}", h.Get)
	r.Post("/v1/onboarding/{key}/approve", h.Approve)
	r.Post("/v1/onboarding/{key}/reject", h.Reject)
	r.Post("/v1/onboarding/reconcile", h.Reconcile)
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

const driverBody = `{"email":"d@example.com","display_name":"Dan Driver","org":"Depot","meta":{"license_no":"DL-482910"}}`

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
		{"invalid meta", "operator", `{"email":"j@example.com","display_name":"Jane","org":"X","meta":notjson}`, http.StatusBadRequest},
		// Wave-8 W8-3: org is mandatory for every gated persona; drivers must
		// also supply meta.license_no.
		{"operator without org", "operator", `{"email":"j@example.com","display_name":"Jane"}`, http.StatusBadRequest},
		{"driver without license", "driver", `{"email":"j@example.com","display_name":"Jane","org":"Depot"}`, http.StatusBadRequest},
		{"driver short license", "driver", `{"email":"j@example.com","display_name":"Jane","org":"Depot","meta":{"license_no":"AB"}}`, http.StatusBadRequest},
		{"valid driver", "driver", driverBody, http.StatusCreated},
		{"valid gov-viewer", "gov-viewer", `{"email":"g@example.com","display_name":"Gov","org":"City Hall"}`, http.StatusCreated},
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

// Wave-8 W8-1: re-filing the same (persona, email) while pending replays the
// existing request (200, deduplicated:true) instead of stacking a duplicate.
func TestIntakeDeduplicatesPending(t *testing.T) {
	_, store, _, router := newTestHandler()
	body := `{"email":"op@example.com","display_name":"Olivia Operator","org":"H2 Ops"}`
	rec := do(t, router, http.MethodPost, "/v1/onboarding/operator", body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("first intake got %d: %s", rec.Code, rec.Body.String())
	}
	firstID := decodeBody(t, rec)["request"].(map[string]any)["id"].(string)

	rec = do(t, router, http.MethodPost, "/v1/onboarding/operator", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("dedup replay got %d want 200: %s", rec.Code, rec.Body.String())
	}
	m := decodeBody(t, rec)
	if m["deduplicated"] != true {
		t.Fatalf("replay must be flagged deduplicated: %v", m)
	}
	if got := m["request"].(map[string]any)["id"]; got != firstID {
		t.Fatalf("replay must return the ORIGINAL request %s, got %v", firstID, got)
	}
	if n := len(store.byID); n != 1 {
		t.Fatalf("dedup must not stack rows, store has %d", n)
	}

	// The same email under a DIFFERENT persona is a distinct request (a
	// person may legitimately apply as driver and advertiser).
	rec = do(t, router, http.MethodPost, "/v1/onboarding/advertiser",
		`{"email":"op@example.com","display_name":"Olivia Operator","org":"H2 Ops"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("different-persona intake got %d: %s", rec.Code, rec.Body.String())
	}
}

// Wave-8 W8-2: more than 5 requests per email per 24 h (across personas) is
// rejected 429 — the gateway's per-IP limit cannot stop address-targeted
// spam on its own.
func TestIntakeVelocityCap(t *testing.T) {
	_, _, _, router := newTestHandler()
	email := "victim@example.com"
	personas := []string{"operator", "advertiser", "data-partner", "gov-viewer", "station-staff"}
	for i, persona := range personas {
		body := fmt.Sprintf(`{"email":%q,"display_name":"Vic Tim","org":"Org %d"}`, email, i)
		rec := do(t, router, http.MethodPost, "/v1/onboarding/"+persona, body)
		if rec.Code != http.StatusCreated {
			t.Fatalf("request %d got %d: %s", i+1, rec.Code, rec.Body.String())
		}
	}
	// The 6th request for the same address — any persona — is capped.
	rec := do(t, router, http.MethodPost, "/v1/onboarding/driver",
		`{"email":"victim@example.com","display_name":"Vic Tim","org":"Depot","meta":{"license_no":"DL-1-2-3"}}`)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("velocity cap got %d want 429: %s", rec.Code, rec.Body.String())
	}
	// A different address is unaffected.
	rec = do(t, router, http.MethodPost, "/v1/onboarding/operator",
		`{"email":"other@example.com","display_name":"Oth Er","org":"Org"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("other email got %d: %s", rec.Code, rec.Body.String())
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
	_, store, kc, router := newTestHandler()
	kc.failCreate = true
	rec := do(t, router, http.MethodPost, "/v1/onboarding/citizen",
		`{"email":"c@example.com","display_name":"Cora Citizen"}`)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("got %d want 502: %s", rec.Code, rec.Body.String())
	}
	// W8-5: the orphaned pending row is marked with provision_error so the
	// retry path (and operators) can see what happened.
	var stored *Request
	for _, r := range store.byID {
		stored = r
	}
	if stored == nil || stored.Status != StatusPending {
		t.Fatalf("failed self-serve must leave a pending row for retry, got %+v", stored)
	}
	var meta map[string]any
	if err := json.Unmarshal(stored.Meta, &meta); err != nil || meta["provision_error"] == nil {
		t.Fatalf("orphan row must carry meta.provision_error: %s", stored.Meta)
	}
}

// Wave-8 W8-5: retrying a failed citizen self-serve adopts the orphaned
// pending row (no duplicate) and completes it (200).
func TestCitizenSelfServeRetryAdoptsOrphan(t *testing.T) {
	_, store, kc, router := newTestHandler()
	kc.failCreate = true
	body := `{"email":"c@example.com","display_name":"Cora Citizen"}`
	if rec := do(t, router, http.MethodPost, "/v1/onboarding/citizen", body); rec.Code != http.StatusBadGateway {
		t.Fatalf("first attempt got %d want 502", rec.Code)
	}
	kc.failCreate = false

	rec := do(t, router, http.MethodPost, "/v1/onboarding/citizen", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("retry got %d want 200: %s", rec.Code, rec.Body.String())
	}
	reqObj := decodeBody(t, rec)["request"].(map[string]any)
	if reqObj["status"] != StatusCompleted || reqObj["keycloak_sub"] != "kc-1" {
		t.Fatalf("retry must complete the adopted row: %v", reqObj)
	}
	if n := len(store.byID); n != 1 {
		t.Fatalf("retry must adopt the orphan, not insert a new row (store has %d)", n)
	}
}

func TestApproveFlow(t *testing.T) {
	_, store, kc, router := newTestHandler()
	// Seed a pending driver request via intake.
	rec := do(t, router, http.MethodPost, "/v1/onboarding/driver", driverBody)
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
		"driver": "driver", "operator": "operator", "station-staff": "station-staff",
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

// Wave-8 W8-9: station-staff provisions its own realm role, no longer the
// full operator role.
func TestApproveProvisionsMappedRole(t *testing.T) {
	_, _, kc, router := newTestHandler()
	rec := do(t, router, http.MethodPost, "/v1/onboarding/station-staff",
		`{"email":"s@example.com","display_name":"Sue Staff","org":"Riverside Station"}`)
	id := decodeBody(t, rec)["request"].(map[string]any)["id"].(string)
	if rec = do(t, router, http.MethodPost, "/v1/onboarding/"+id+"/approve", ""); rec.Code != http.StatusOK {
		t.Fatalf("approve got %d: %s", rec.Code, rec.Body.String())
	}
	if got := kc.assignedRoles["kc-1"]; len(got) != 1 || got[0] != "station-staff" {
		t.Fatalf("station-staff must map to the station-staff realm role, got %v", got)
	}
}

// Operators (and any non-platform-admin) may list/view onboarding requests
// but must NOT be able to approve or reject them — approving an operator or
// station-staff intake would let one operator mint further privileged
// accounts (SECURITY_AUDIT F3, privilege self-replication).
func TestOperatorCannotDecide(t *testing.T) {
	store := newFakeStore()
	kc := newFakeKC()
	h := NewHandler(store, kc, zap.NewNop(), func() string { return "TmpPassw0rd!" })
	adminRouter := newTestRouter(h, "admin-1", "platform-admin")
	operatorRouter := newTestRouter(h, "op-1", "operator")

	// Seed a pending operator intake (the most dangerous persona).
	rec := do(t, adminRouter, http.MethodPost, "/v1/onboarding/operator",
		`{"email":"o@example.com","display_name":"Otto Operator","org":"H2 Ops"}`)
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
		`{"email":"a@example.com","display_name":"Ad Annie","org":"Ads R Us"}`)
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

// Wave-8 W8-6: the applicant follows their request via the public
// capability URL. The response carries status and timestamps only — never
// email, name, org or meta.
func TestStatusPublic(t *testing.T) {
	_, _, _, router := newTestHandler()
	rec := do(t, router, http.MethodPost, "/v1/onboarding/operator",
		`{"email":"secret-applicant@example.com","display_name":"Private Person","org":"H2 Ops"}`)
	id := decodeBody(t, rec)["request"].(map[string]any)["id"].(string)

	rec = do(t, router, http.MethodGet, "/v1/onboarding/status/"+id, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status check got %d: %s", rec.Code, rec.Body.String())
	}
	m := decodeBody(t, rec)
	if m["status"] != StatusPending || m["persona"] != "operator" || m["id"] != id {
		t.Fatalf("unexpected status payload: %v", m)
	}
	body := rec.Body.String()
	for _, pii := range []string{"secret-applicant@example.com", "Private Person", "H2 Ops", "meta", "email", "display_name"} {
		if strings.Contains(body, pii) {
			t.Fatalf("public status must not leak %q (body: %s)", pii, body)
		}
	}

	// Unknown ids 404 (existence not confirmed either way).
	if rec = do(t, router, http.MethodGet, "/v1/onboarding/status/nope", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown status id got %d want 404", rec.Code)
	}
}

// Wave-8 W8-4: reconcile re-asserts persona roles on completed requests,
// repairing users stranded by a mid-sequence provisioning failure.
func TestReconcile(t *testing.T) {
	store := newFakeStore()
	kc := newFakeKC()
	h := NewHandler(store, kc, zap.NewNop(), func() string { return "TmpPassw0rd!" })
	adminRouter := newTestRouter(h, "admin-1", "platform-admin")
	operatorRouter := newTestRouter(h, "op-1", "operator")

	// Complete one driver request via the normal approve flow.
	rec := do(t, adminRouter, http.MethodPost, "/v1/onboarding/driver", driverBody)
	id := decodeBody(t, rec)["request"].(map[string]any)["id"].(string)
	if rec = do(t, adminRouter, http.MethodPost, "/v1/onboarding/"+id+"/approve", ""); rec.Code != http.StatusOK {
		t.Fatalf("approve got %d: %s", rec.Code, rec.Body.String())
	}
	// Simulate the stranded user: drop the role behind the store's back.
	kc.assignedRoles["kc-1"] = nil

	// Operators must not reconcile (it touches identity state).
	if rec = do(t, operatorRouter, http.MethodPost, "/v1/onboarding/reconcile", ""); rec.Code != http.StatusForbidden {
		t.Fatalf("operator reconcile got %d want 403", rec.Code)
	}

	rec = do(t, adminRouter, http.MethodPost, "/v1/onboarding/reconcile", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("reconcile got %d: %s", rec.Code, rec.Body.String())
	}
	m := decodeBody(t, rec)
	if m["checked"].(float64) != 1 || m["ensured"].(float64) != 1 || m["failed"].(float64) != 0 {
		t.Fatalf("unexpected reconcile summary: %v", m)
	}
	if got := kc.assignedRoles["kc-1"]; len(got) != 1 || got[0] != "driver" {
		t.Fatalf("reconcile must restore the driver role, got %v", got)
	}

	// Keycloak failures are reported, not hidden.
	kc.failEnsure = true
	rec = do(t, adminRouter, http.MethodPost, "/v1/onboarding/reconcile", "")
	m = decodeBody(t, rec)
	if rec.Code != http.StatusOK || m["failed"].(float64) != 1 {
		t.Fatalf("reconcile failures must surface in the summary: %d %v", rec.Code, m)
	}
}

// Wave-8 W8-7: a request pending longer than the TTL can no longer be
// decided — it is expired on the spot (409), and the sweep flips stragglers.
func TestPendingTTLExpiry(t *testing.T) {
	store := newFakeStore()
	kc := newFakeKC()
	h := NewHandler(store, kc, zap.NewNop(), func() string { return "TmpPassw0rd!" })
	h.PendingTTL = time.Hour
	router := newTestRouter(h, "admin-1", "platform-admin")

	store.seed(&Request{
		ID: "req-old", Persona: "operator", Email: "old@example.com",
		DisplayName: "Ol D", Org: "H2 Ops", Status: StatusPending,
		CreatedAt: time.Now().Add(-2 * time.Hour).UTC(),
	})

	rec := do(t, router, http.MethodPost, "/v1/onboarding/req-old/approve", "")
	if rec.Code != http.StatusConflict {
		t.Fatalf("approve of stale request got %d want 409: %s", rec.Code, rec.Body.String())
	}
	stored, _ := store.Get(context.Background(), "req-old")
	if stored.Status != StatusExpired {
		t.Fatalf("stale request must be expired by the decide guard, got %s", stored.Status)
	}
	if len(kc.created) != 0 {
		t.Fatalf("expired request must not be provisioned")
	}

	// The sweep expires older pendings in bulk; fresh ones survive.
	store.seed(&Request{
		ID: "req-old2", Persona: "driver", Email: "old2@example.com",
		DisplayName: "Ol Der", Org: "Depot", Status: StatusPending,
		CreatedAt: time.Now().Add(-48 * time.Hour).UTC(),
	})
	store.seed(&Request{
		ID: "req-fresh", Persona: "driver", Email: "fresh@example.com",
		DisplayName: "Fre Sh", Org: "Depot", Status: StatusPending,
		CreatedAt: time.Now().UTC(),
	})
	n, err := store.ExpirePending(context.Background(), time.Now().Add(-24*time.Hour))
	if err != nil || n != 1 {
		t.Fatalf("sweep expired %d (err %v), want 1", n, err)
	}
	if r, _ := store.Get(context.Background(), "req-fresh"); r.Status != StatusPending {
		t.Fatalf("fresh request must survive the sweep, got %s", r.Status)
	}
}

// Wave-8 W8-8: with a captcha configured the public intake requires a valid
// token (fail-closed on verifier errors).
func TestCaptchaGate(t *testing.T) {
	var gotSecret string
	verify := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		gotSecret = r.Form.Get("secret")
		w.Header().Set("Content-Type", "application/json")
		if r.Form.Get("response") == "good-token" {
			_, _ = w.Write([]byte(`{"success":true}`))
			return
		}
		_, _ = w.Write([]byte(`{"success":false}`))
	}))
	defer verify.Close()

	store := newFakeStore()
	h := NewHandler(store, newFakeKC(), zap.NewNop(), func() string { return "TmpPassw0rd!" })
	h.Captcha = &CaptchaConfig{VerifyURL: verify.URL, Secret: "test-secret"}
	router := newTestRouter(h, "admin-1", "platform-admin")
	body := `{"email":"cap@example.com","display_name":"Cap Tcha","org":"H2 Ops"}`

	if rec := do(t, router, http.MethodPost, "/v1/onboarding/operator", body); rec.Code != http.StatusBadRequest {
		t.Fatalf("missing captcha token got %d want 400", rec.Code)
	}
	if rec := do(t, router, http.MethodPost, "/v1/onboarding/operator",
		strings.Replace(body, `}`, `,"captcha_token":"bad-token"}`, -1)); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad captcha token got %d want 400", rec.Code)
	}
	rec := do(t, router, http.MethodPost, "/v1/onboarding/operator",
		strings.Replace(body, `}`, `,"captcha_token":"good-token"}`, -1))
	if rec.Code != http.StatusCreated {
		t.Fatalf("good captcha token got %d: %s", rec.Code, rec.Body.String())
	}
	if gotSecret != "test-secret" {
		t.Fatalf("verifier must receive the configured secret, got %q", gotSecret)
	}

	// Verifier unreachable → fail closed (502), nothing stored.
	h.Captcha.VerifyURL = "http://127.0.0.1:1/unreachable"
	if rec = do(t, router, http.MethodPost, "/v1/onboarding/advertiser",
		`{"email":"cap2@example.com","display_name":"Cap Two","org":"Org","captcha_token":"x"}`); rec.Code != http.StatusBadGateway {
		t.Fatalf("unreachable verifier got %d want 502", rec.Code)
	}
}

func TestListFilters(t *testing.T) {
	_, _, _, router := newTestHandler()
	do(t, router, http.MethodPost, "/v1/onboarding/driver", driverBody)
	do(t, router, http.MethodPost, "/v1/onboarding/operator", `{"email":"o@example.com","display_name":"O","org":"H2 Ops"}`)
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

	// Wave-8: 'expired' is a valid filter value.
	if rec = do(t, router, http.MethodGet, "/v1/onboarding?status=expired", ""); rec.Code != http.StatusOK {
		t.Fatalf("expired status filter got %d want 200", rec.Code)
	}
	if rec = do(t, router, http.MethodGet, "/v1/onboarding?status=bogus", ""); rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid status filter got %d want 400", rec.Code)
	}
}

// --------------------------------------------------------------------------
// Wave-9 audit regression tests
// --------------------------------------------------------------------------

// W9-1 (critical): citizen self-serve for an email that ALREADY has a
// Keycloak account must never reset that account's credentials. Before the
// fix, provision() unconditionally SetTemporaryPassword on the adopted id —
// an unauthenticated password-reset DoS against any registered address
// (including operators). Now: no password touch, role ensured, actions
// email to the address on file, indistinguishable 201.
func TestCitizenSelfServeNeverResetsExistingAccount(t *testing.T) {
	_, _, kc, router := newTestHandler()
	kc.seedExisting("victim@example.com", "kc-victim")

	rec := do(t, router, http.MethodPost, "/v1/onboarding/citizen",
		`{"email":"victim@example.com","display_name":"Attacker Name"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("got %d: %s", rec.Code, rec.Body.String())
	}
	if _, reset := kc.passwords["kc-victim"]; reset {
		t.Fatalf("existing account's password was reset — credential-reset DoS regression")
	}
	if got := kc.assignedRoles["kc-victim"]; len(got) != 1 || got[0] != "citizen" {
		t.Fatalf("citizen role must be ensured on the existing account, got %v", got)
	}
	if len(kc.actionsSent["kc-victim"]) == 0 {
		t.Fatalf("actions email (ownership-proof channel) must still be sent")
	}
	// The response is indistinguishable from a fresh registration (no
	// account-existence oracle).
	m := decodeBody(t, rec)
	if m["existing_account"] != nil {
		t.Fatalf("citizen response must not disclose account existence: %v", m)
	}
}

// W9-1: the approve path likewise must not reset a pre-existing account's
// password — but the approving admin IS told (existing_account:true), since
// an intake filed with someone else's email may be impersonation.
func TestApproveExistingAccountNoResetButFlagged(t *testing.T) {
	_, _, kc, router := newTestHandler()
	kc.seedExisting("boss@example.com", "kc-boss")

	rec := do(t, router, http.MethodPost, "/v1/onboarding/driver",
		`{"email":"boss@example.com","display_name":"Boss","org":"Depot","meta":{"license_no":"DL-998877"}}`)
	id := decodeBody(t, rec)["request"].(map[string]any)["id"].(string)

	rec = do(t, router, http.MethodPost, "/v1/onboarding/"+id+"/approve", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("approve got %d: %s", rec.Code, rec.Body.String())
	}
	if m := decodeBody(t, rec); m["existing_account"] != true {
		t.Fatalf("approve must flag existing_account=true, got %v", m)
	}
	if _, reset := kc.passwords["kc-boss"]; reset {
		t.Fatalf("existing account's password was reset on approve")
	}
	if got := kc.assignedRoles["kc-boss"]; len(got) != 1 || got[0] != "driver" {
		t.Fatalf("driver role must be ensured, got %v", got)
	}
}

// W9-2: email identity is case-insensitive — case variants of one address
// dedup to the same pending request and share the velocity budget.
func TestEmailCaseInsensitiveDedupAndVelocity(t *testing.T) {
	_, store, _, router := newTestHandler()

	rec := do(t, router, http.MethodPost, "/v1/onboarding/operator",
		`{"email":"CaseTest@Example.COM","display_name":"Case Test","org":"H2 Ops"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("first intake got %d: %s", rec.Code, rec.Body.String())
	}
	// Stored lowercase (canonical form).
	for _, r := range store.byID {
		if r.Email != "casetest@example.com" {
			t.Fatalf("email must be stored lowercase, got %q", r.Email)
		}
	}
	// Same address, different case → dedup replay, not a second row.
	rec = do(t, router, http.MethodPost, "/v1/onboarding/operator",
		`{"email":"casetest@example.com","display_name":"Case Test","org":"H2 Ops"}`)
	if rec.Code != http.StatusOK || decodeBody(t, rec)["deduplicated"] != true {
		t.Fatalf("case-variant intake must dedup-replay, got %d %s", rec.Code, rec.Body.String())
	}

	// Velocity: case variants of another address share one budget.
	personas := []string{"operator", "advertiser", "data-partner", "gov-viewer", "station-staff"}
	emails := []string{"Vic@Example.com", "vic@example.COM", "VIC@example.com", "vic@example.com", "vIc@Example.com"}
	for i, persona := range personas {
		body := fmt.Sprintf(`{"email":%q,"display_name":"Vic","org":"Org"}`, emails[i])
		if rec = do(t, router, http.MethodPost, "/v1/onboarding/"+persona, body); rec.Code != http.StatusCreated {
			t.Fatalf("request %d got %d: %s", i, rec.Code, rec.Body.String())
		}
	}
	// The 6th request for the same address — in yet another case — is capped.
	rec = do(t, router, http.MethodPost, "/v1/onboarding/driver",
		`{"email":"vIc@eXample.com","display_name":"Vic","org":"Depot","meta":{"license_no":"DL-1111"}}`)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("case-variant velocity bypass: got %d want 429", rec.Code)
	}
}

// W9-6: intake hard limits — oversized email/meta are rejected.
func TestIntakeHardLimits(t *testing.T) {
	_, _, _, router := newTestHandler()
	longEmail := strings.Repeat("a", 245) + "@example.com" // 257 chars
	rec := do(t, router, http.MethodPost, "/v1/onboarding/operator",
		fmt.Sprintf(`{"email":%q,"display_name":"A","org":"O"}`, longEmail))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("oversized email got %d want 400", rec.Code)
	}
	bigMeta := `{"blob":"` + strings.Repeat("x", 9000) + `"}`
	rec = do(t, router, http.MethodPost, "/v1/onboarding/operator",
		`{"email":"a@example.com","display_name":"A","org":"O","meta":`+bigMeta+`}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("oversized meta got %d want 400", rec.Code)
	}
}

// W9-6: reject reasons are length-capped.
func TestRejectReasonCap(t *testing.T) {
	_, _, _, router := newTestHandler()
	rec := do(t, router, http.MethodPost, "/v1/onboarding/operator",
		`{"email":"r@example.com","display_name":"R","org":"O"}`)
	id := decodeBody(t, rec)["request"].(map[string]any)["id"].(string)
	rec = do(t, router, http.MethodPost, "/v1/onboarding/"+id+"/reject",
		`{"reason":"`+strings.Repeat("x", 501)+`"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("501-char reason got %d want 400", rec.Code)
	}
	rec = do(t, router, http.MethodPost, "/v1/onboarding/"+id+"/reject",
		`{"reason":"duplicate"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("normal reject got %d: %s", rec.Code, rec.Body.String())
	}
}
