package gate

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	toggle "github.com/munisp/hydrogenTransport/packages/toggle-client/go"
)

// moduleUnderTest is a representative module gate of this service:
// fleet-api gates the vehicles routes behind the telematics module.
const (
	moduleUnderTest = "telematics"
	routeUnderTest  = "/v1/vehicles"
)

// toggleServer stubs the toggle-service REST contract (SPEC §3.2):
// GET /v1/toggles/{module} -> {"module","enabled","domain"}.
func toggleServer(t *testing.T, enabled bool) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		module := strings.TrimPrefix(r.URL.Path, "/v1/toggles/")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"module": module, "enabled": enabled, "domain": "fleet",
		})
	}))
	t.Cleanup(srv.Close)
	return srv
}

func gatedRouter(toggleURL string) *chi.Mux {
	r := chi.NewRouter()
	r.With(Module(toggle.New(toggleURL), moduleUnderTest)).Get(routeUnderTest,
		func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	return r
}

func get(t *testing.T, h http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

// Gate contract: a toggled-OFF module route must return 404 (SPEC §3.2).
func TestModule_ToggledOffReturns404(t *testing.T) {
	srv := toggleServer(t, false)
	rec := get(t, gatedRouter(srv.URL), routeUnderTest)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("toggled-off module route: got %d, want 404", rec.Code)
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("404 body is not JSON: %v", err)
	}
	if !strings.Contains(body["error"], moduleUnderTest) {
		t.Fatalf("404 body should name the module, got %q", body["error"])
	}
}

// Gate contract: a toggled-ON module route must reach its handler.
func TestModule_ToggledOnPassesThrough(t *testing.T) {
	srv := toggleServer(t, true)
	rec := get(t, gatedRouter(srv.URL), routeUnderTest)

	if rec.Code != http.StatusOK {
		t.Fatalf("toggled-on module route: got %d, want 200", rec.Code)
	}
}

// Gate contract: the toggle client is fail-closed (SPEC §3.2), so a
// toggle-service outage must also yield 404, never accidentally enable.
func TestModule_ToggleOutageIsFailClosed(t *testing.T) {
	srv := toggleServer(t, true)
	srv.Close() // simulate toggle-service down

	rec := get(t, gatedRouter(srv.URL), routeUnderTest)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("toggle outage must be fail-closed: got %d, want 404", rec.Code)
	}
}
