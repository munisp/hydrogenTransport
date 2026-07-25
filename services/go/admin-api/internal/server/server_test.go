package server_test

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"go.uber.org/zap"

	auth "github.com/munisp/hydrogenTransport/packages/go-auth"
	"github.com/munisp/hydrogenTransport/services/go/admin-api/internal/keycloak"
	"github.com/munisp/hydrogenTransport/services/go/admin-api/internal/kpi"
	"github.com/munisp/hydrogenTransport/services/go/admin-api/internal/onboarding"
	"github.com/munisp/hydrogenTransport/services/go/admin-api/internal/ops"
	"github.com/munisp/hydrogenTransport/services/go/admin-api/internal/server"
	"github.com/munisp/hydrogenTransport/services/go/admin-api/internal/users"
)

// --------------------------------------------------------------------------
// mock JWKS + token signing
// --------------------------------------------------------------------------

type jwksEnv struct {
	issuer string
	key    *rsa.PrivateKey
	srv    *httptest.Server
}

func newJWKS(t *testing.T) *jwksEnv {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	env := &jwksEnv{key: key}
	mux := http.NewServeMux()
	// auth.New derives the JWKS URL as <issuer>/protocol/openid-connect/certs.
	mux.HandleFunc("/realms/h2fleet/protocol/openid-connect/certs", func(w http.ResponseWriter, r *http.Request) {
		n := base64.RawURLEncoding.EncodeToString(key.PublicKey.N.Bytes())
		e := base64.RawURLEncoding.EncodeToString([]byte{1, 0, 1}) // 65537
		fmt.Fprintf(w, `{"keys":[{"kty":"RSA","kid":"test","use":"sig","alg":"RS256","n":%q,"e":%q}]}`, n, e)
	})
	env.srv = httptest.NewServer(mux)
	t.Cleanup(env.srv.Close)
	env.issuer = env.srv.URL + "/realms/h2fleet"
	return env
}

func (e *jwksEnv) token(t *testing.T, sub string, roles ...string) string {
	t.Helper()
	claims := jwt.MapClaims{
		"iss":          e.issuer,
		"sub":          sub,
		"exp":          time.Now().Add(time.Hour).Unix(),
		"iat":          time.Now().Unix(),
		"realm_access": map[string]any{"roles": roles},
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	tok.Header["kid"] = "test"
	signed, err := tok.SignedString(e.key)
	if err != nil {
		t.Fatalf("sign token: %v", err)
	}
	return signed
}

// --------------------------------------------------------------------------
// fakes
// --------------------------------------------------------------------------

type fakeStore struct {
	mu  sync.Mutex
	seq int
	req map[string]*onboarding.Request
}

func (s *fakeStore) EnsureSchema(context.Context) error { return nil }
func (s *fakeStore) Ping(context.Context) error         { return nil }
func (s *fakeStore) Create(_ context.Context, r *onboarding.Request) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seq++
	r.ID = fmt.Sprintf("req-%d", s.seq)
	r.CreatedAt = time.Now().UTC()
	if s.req == nil {
		s.req = map[string]*onboarding.Request{}
	}
	s.req[r.ID] = r
	return nil
}
func (s *fakeStore) Get(_ context.Context, id string) (*onboarding.Request, error) {
	if r, ok := s.req[id]; ok {
		return r, nil
	}
	return nil, onboarding.ErrNotFound
}
func (s *fakeStore) List(context.Context, string, string, int) ([]onboarding.Request, error) {
	return []onboarding.Request{}, nil
}
func (s *fakeStore) Decide(_ context.Context, id, status, kcSub, by, reason string) (*onboarding.Request, error) {
	r := s.req[id]
	r.Status = status
	r.KeycloakSub = kcSub
	r.DecidedBy = by
	return r, nil
}

type fakeSource struct{ name string }

func (f fakeSource) Name() string { return f.name }
func (f fakeSource) Collect(context.Context) (any, error) {
	return map[string]int{"ok": 1}, nil
}

// --------------------------------------------------------------------------
// harness
// --------------------------------------------------------------------------

func buildRouter(t *testing.T, env *jwksEnv) http.Handler {
	t.Helper()
	log := zap.NewNop()
	jwtmw := auth.New(env.issuer, log)
	kc := keycloak.New("http://127.0.0.1:1", "h2fleet", "", "", log) // simulated dev client

	return server.NewRouter(server.Deps{
		Log: log,
		JWT: jwtmw,
		Healthz: func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		},
		Onboarding: onboarding.NewHandler(&fakeStore{}, kc, log, func() string { return "pw" }),
		Users:      users.NewHandler(kc, log),
		KPIs: kpi.NewAggregator([]kpi.Source{
			fakeSource{"fleet"}, fakeSource{"infra"}, fakeSource{"citizen"},
			fakeSource{"commerce"}, fakeSource{"toggles"},
		}, time.Second),
		Ops: ops.NewHandler(nil, "http://127.0.0.1:1", "http://127.0.0.1:1", log),
	})
}

func call(t *testing.T, router http.Handler, method, path, body, token string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

// --------------------------------------------------------------------------
// role-gate tests
// --------------------------------------------------------------------------

func TestRoleGates(t *testing.T) {
	env := newJWKS(t)
	router := buildRouter(t, env)

	admin := env.token(t, "admin-1", "platform-admin")
	operator := env.token(t, "op-1", "operator")
	citizen := env.token(t, "cit-1", "citizen")

	cases := []struct {
		name   string
		method string
		path   string
		body   string
		token  string
		want   int
	}{
		// 401s: protected routes without a token.
		{"users list unauthenticated", "GET", "/v1/users", "", "", http.StatusUnauthorized},
		{"kpis unauthenticated", "GET", "/v1/admin/kpis", "", "", http.StatusUnauthorized},
		{"health unauthenticated", "GET", "/v1/admin/health", "", "", http.StatusUnauthorized},
		{"onboarding list unauthenticated", "GET", "/v1/onboarding", "", "", http.StatusUnauthorized},

		// 403s: authenticated but missing the required role.
		{"users list as citizen", "GET", "/v1/users", "", citizen, http.StatusForbidden},
		{"users list as operator", "GET", "/v1/users", "", operator, http.StatusForbidden},
		{"kpis as citizen", "GET", "/v1/admin/kpis", "", citizen, http.StatusForbidden},
		{"toggle put as operator", "PUT", "/v1/admin/toggles/telematics", `{"enabled":false}`, operator, http.StatusForbidden},

		// 200s: correct roles.
		{"users list as admin", "GET", "/v1/users", "", admin, http.StatusOK},
		{"kpis as admin", "GET", "/v1/admin/kpis", "", admin, http.StatusOK},
		{"kpis as operator", "GET", "/v1/admin/kpis", "", operator, http.StatusOK},
		{"health as operator", "GET", "/v1/admin/health", "", operator, http.StatusOK},
		{"alerts as operator", "GET", "/v1/admin/alerts", "", operator, http.StatusOK},
		{"onboarding list as operator", "GET", "/v1/onboarding", "", operator, http.StatusOK},

		// Public surfaces need no token.
		{"healthz public", "GET", "/healthz", "", "", http.StatusOK},
		{"citizen self-serve public", "POST", "/v1/onboarding/citizen",
			`{"email":"c@example.com","display_name":"Cora"}`, "", http.StatusCreated},
		{"intake public", "POST", "/v1/onboarding/driver",
			`{"email":"d@example.com","display_name":"Dan"}`, "", http.StatusCreated},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := call(t, router, tc.method, tc.path, tc.body, tc.token)
			if rec.Code != tc.want {
				t.Fatalf("got %d want %d body=%s", rec.Code, tc.want, rec.Body.String())
			}
		})
	}
}

func TestApproveGateAndFlow(t *testing.T) {
	env := newJWKS(t)
	router := buildRouter(t, env)

	// Create a pending request via public intake.
	rec := call(t, router, "POST", "/v1/onboarding/driver",
		`{"email":"d@example.com","display_name":"Dan"}`, "")
	if rec.Code != http.StatusCreated {
		t.Fatalf("intake: %d %s", rec.Code, rec.Body.String())
	}
	var created struct {
		Request struct {
			ID string `json:"id"`
		} `json:"request"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode intake: %v", err)
	}
	id := created.Request.ID

	// citizen cannot approve.
	if rec := call(t, router, "POST", "/v1/onboarding/"+id+"/approve", "", env.token(t, "c", "citizen")); rec.Code != http.StatusForbidden {
		t.Fatalf("citizen approve got %d want 403", rec.Code)
	}
	// operator CANNOT approve either (F3): onboarding decisions are
	// platform-admin only, even though operators may list/view the queue.
	if rec := call(t, router, "POST", "/v1/onboarding/"+id+"/approve", "", env.token(t, "o", "operator")); rec.Code != http.StatusForbidden {
		t.Fatalf("operator approve got %d want 403", rec.Code)
	}
	if rec := call(t, router, "POST", "/v1/onboarding/"+id+"/reject", `{"reason":"x"}`, env.token(t, "o", "operator")); rec.Code != http.StatusForbidden {
		t.Fatalf("operator reject got %d want 403", rec.Code)
	}
	// platform-admin CAN approve.
	rec = call(t, router, "POST", "/v1/onboarding/"+id+"/approve", "", env.token(t, "a", "platform-admin"))
	if rec.Code != http.StatusOK {
		t.Fatalf("platform-admin approve got %d: %s", rec.Code, rec.Body.String())
	}
	var decided struct {
		Request struct {
			Status    string `json:"status"`
			DecidedBy string `json:"decided_by"`
		} `json:"request"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &decided); err != nil {
		t.Fatalf("decode approve: %v", err)
	}
	if decided.Request.Status != "completed" {
		t.Fatalf("status = %q want completed", decided.Request.Status)
	}
	if decided.Request.DecidedBy != "a" {
		t.Fatalf("decided_by should be the token sub, got %q", decided.Request.DecidedBy)
	}
}

func TestChiRouteCoexistence(t *testing.T) {
	// Static /v1/onboarding/citizen must not be swallowed by the {key}
	// wildcard, and the deeper approve path must route correctly.
	env := newJWKS(t)
	router := buildRouter(t, env)
	rec := call(t, router, "POST", "/v1/onboarding/citizen",
		`{"email":"x@example.com","display_name":"X"}`, "")
	if rec.Code != http.StatusCreated {
		t.Fatalf("citizen self-serve shadowed by wildcard: %d %s", rec.Code, rec.Body.String())
	}
}
