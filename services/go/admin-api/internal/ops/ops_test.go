package ops

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"go.uber.org/zap"
)

func ioReader(s string) io.Reader { return strings.NewReader(s) }

// newToggleMux registers UpdateToggle behind a chi router so {module} URL
// params are populated.
func newToggleMux(h *Handler) http.Handler {
	r := chi.NewRouter()
	r.Put("/v1/admin/toggles/{module}", h.UpdateToggle)
	return r
}

func TestSweepHealth(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/healthz" {
			t.Fatalf("unexpected probe path %s", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer up.Close()
	sick := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer sick.Close()

	// TCP listener that accepts; and a closed TCP port.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			_ = c.Close()
		}
	}()
	closedLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	closedAddr := closedLn.Addr().String()
	_ = closedLn.Close()

	targets := []Target{
		{Name: "fleet-api", Kind: "http", Addr: up.URL},
		{Name: "infra-api", Kind: "http", Addr: sick.URL},
		{Name: "ghost", Kind: "http", Addr: "http://127.0.0.1:1"},
		{Name: "postgres", Kind: "tcp", Addr: ln.Addr().String()},
		{Name: "kafka", Kind: "tcp", Addr: closedAddr},
	}
	resp := SweepHealth(context.Background(), targets)
	if len(resp.Checks) != len(targets) {
		t.Fatalf("expected %d checks, got %d", len(targets), len(resp.Checks))
	}
	byName := map[string]Check{}
	for _, c := range resp.Checks {
		byName[c.Name] = c
		if c.Status != "up" && c.Status != "down" {
			t.Fatalf("bad status %q", c.Status)
		}
	}
	if byName["fleet-api"].Status != "up" {
		t.Fatalf("healthy service reported down: %+v", byName["fleet-api"])
	}
	if byName["infra-api"].Status != "down" {
		t.Fatalf("503 service reported up")
	}
	if byName["ghost"].Status != "down" {
		t.Fatalf("unreachable service reported up")
	}
	if byName["postgres"].Status != "up" {
		t.Fatalf("reachable TCP endpoint reported down")
	}
	if byName["kafka"].Status != "down" {
		t.Fatalf("closed TCP endpoint reported up")
	}
	if resp.Summary.Up != 2 || resp.Summary.Down != 3 {
		t.Fatalf("bad summary: %+v", resp.Summary)
	}
	// Payload shape: [{name, status, latency_ms, ...}] entries.
	buf, _ := json.Marshal(resp)
	var decoded struct {
		Checks []struct {
			Name      string `json:"name"`
			Status    string `json:"status"`
			LatencyMs int64  `json:"latency_ms"`
		} `json:"checks"`
	}
	if err := json.Unmarshal(buf, &decoded); err != nil || len(decoded.Checks) != 5 {
		t.Fatalf("payload shape broken: %v %s", err, buf)
	}
}

func TestAlertsProxyPassthrough(t *testing.T) {
	payload := `[{"labels":{"alertname":"HighLatency"},"status":{"state":"active"}}]`
	am := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v2/alerts" {
			t.Fatalf("unexpected alertmanager path %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(payload))
	}))
	defer am.Close()

	h := NewHandler(nil, am.URL, "http://127.0.0.1:1", zap.NewNop())
	rec := httptest.NewRecorder()
	h.Alerts(rec, httptest.NewRequest(http.MethodGet, "/v1/admin/alerts", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d", rec.Code)
	}
	if rec.Body.String() != payload {
		t.Fatalf("alerts not proxied verbatim: %s", rec.Body.String())
	}
}

func TestAlertsGracefulWhenAlertmanagerDown(t *testing.T) {
	h := NewHandler(nil, "http://127.0.0.1:1", "http://127.0.0.1:1", zap.NewNop())
	rec := httptest.NewRecorder()
	h.Alerts(rec, httptest.NewRequest(http.MethodGet, "/v1/admin/alerts", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d want 200", rec.Code)
	}
	var arr []any
	if err := json.Unmarshal(rec.Body.Bytes(), &arr); err != nil || len(arr) != 0 {
		t.Fatalf("expected empty array, got %s", rec.Body.String())
	}
}

func TestListTogglesEnriched(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"toggles": {"telematics": true, "fare-payments": false}}`))
	}))
	defer ts.Close()

	h := NewHandler(nil, "http://127.0.0.1:1", ts.URL, zap.NewNop())
	rec := httptest.NewRecorder()
	h.ListToggles(rec, httptest.NewRequest(http.MethodGet, "/v1/admin/toggles", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d", rec.Code)
	}
	var body struct {
		Toggles []struct {
			Module         string   `json:"module"`
			Domain         string   `json:"domain"`
			Enabled        bool     `json:"enabled"`
			OwningServices []string `json:"owning_services"`
		} `json:"toggles"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Toggles) != 2 {
		t.Fatalf("expected 2 toggles, got %d", len(body.Toggles))
	}
	// Sorted by domain: commerce (fare-payments) before fleet (telematics).
	if body.Toggles[0].Module != "fare-payments" || body.Toggles[0].Domain != "commerce" || body.Toggles[0].Enabled {
		t.Fatalf("bad first toggle: %+v", body.Toggles[0])
	}
	if body.Toggles[1].Module != "telematics" || body.Toggles[1].Domain != "fleet" || !body.Toggles[1].Enabled {
		t.Fatalf("bad second toggle: %+v", body.Toggles[1])
	}
	if len(body.Toggles[1].OwningServices) == 0 || body.Toggles[1].OwningServices[0] != "fleet-api" {
		t.Fatalf("owning services missing: %+v", body.Toggles[1])
	}
}

func TestUpdateToggleProxyForwardsJWT(t *testing.T) {
	var gotAuth, gotBody string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut || r.URL.Path != "/v1/toggles/telematics" {
			t.Fatalf("unexpected upstream call %s %s", r.Method, r.URL.Path)
		}
		gotAuth = r.Header.Get("Authorization")
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"module":"telematics","enabled":false,"domain":"fleet"}`))
	}))
	defer ts.Close()

	h := NewHandler(nil, "http://127.0.0.1:1", ts.URL, zap.NewNop())
	req := httptest.NewRequest(http.MethodPut, "/v1/admin/toggles/telematics", ioReader(`{"enabled":false}`))
	req.Header.Set("Authorization", "Bearer caller-jwt")
	rec := httptest.NewRecorder()
	// chi route context is needed for URLParam; wrap in a chi router.
	mux := newToggleMux(h)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("got %d: %s", rec.Code, rec.Body.String())
	}
	if gotAuth != "Bearer caller-jwt" {
		t.Fatalf("caller JWT not forwarded: %q", gotAuth)
	}
	if gotBody != `{"enabled":false}` {
		t.Fatalf("body not forwarded: %q", gotBody)
	}
	if rec.Body.String() != `{"module":"telematics","enabled":false,"domain":"fleet"}` {
		t.Fatalf("upstream response not proxied: %s", rec.Body.String())
	}
}

func TestUpdateToggleUnknownModule(t *testing.T) {
	h := NewHandler(nil, "http://127.0.0.1:1", "http://127.0.0.1:1", zap.NewNop())
	req := httptest.NewRequest(http.MethodPut, "/v1/admin/toggles/bogus", ioReader(`{"enabled":true}`))
	rec := httptest.NewRecorder()
	newToggleMux(h).ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("got %d want 404", rec.Code)
	}
}

func TestSweepHealthWithinTimeout(t *testing.T) {
	// A silent TCP listener (accepts but the HTTP handler stalls) must not
	// stall the whole sweep beyond the per-check timeout.
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(3 * time.Second)
	}))
	defer slow.Close()
	start := time.Now()
	resp := SweepHealth(context.Background(), []Target{{Name: "slow", Kind: "http", Addr: slow.URL}})
	if elapsed := time.Since(start); elapsed > 4*time.Second {
		t.Fatalf("sweep exceeded per-check timeout: %v", elapsed)
	}
	if resp.Checks[0].Status != "down" {
		t.Fatalf("timed-out target must be down")
	}
}
