package auditclient

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/go-chi/chi/v5"
	"go.uber.org/zap"
)

type captured struct {
	mu      sync.Mutex
	entries []Entry
	token   string
}

func (c *captured) server(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c.mu.Lock()
		defer c.mu.Unlock()
		c.token = r.Header.Get("X-Audit-Token")
		var e Entry
		if err := json.NewDecoder(r.Body).Decode(&e); err != nil {
			http.Error(w, "bad body", http.StatusBadRequest)
			return
		}
		c.entries = append(c.entries, e)
		w.WriteHeader(http.StatusCreated)
	}))
}

func (c *captured) last() Entry {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.entries[len(c.entries)-1]
}

func (c *captured) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.entries)
}

func TestDisabledClientIsNoop(t *testing.T) {
	c := New("", "tok", "svc", zap.NewNop())
	if c.Enabled() {
		t.Fatal("empty base URL must disable the client")
	}
	c.Emit(httptest.NewRequest(http.MethodGet, "/", nil).Context(), Entry{Action: "x"})
	// Middleware must pass through untouched.
	ran := false
	h := c.Middleware("a", "e", "", false)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		ran = true
		w.WriteHeader(http.StatusOK)
	}))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPut, "/", nil))
	if !ran {
		t.Fatal("disabled middleware must pass through")
	}
}

func TestEmitPostsEntryWithToken(t *testing.T) {
	cap := &captured{}
	srv := cap.server(t)
	defer srv.Close()

	c := New(srv.URL, "s3cret", "commerce-api", zap.NewNop())
	c.Emit(httptest.NewRequest(http.MethodGet, "/", nil).Context(), Entry{
		ActorSub: "svc", Action: "payment.create", Entity: "fare_payment", EntityID: "p1",
	})
	if cap.count() != 1 {
		t.Fatalf("entries = %d, want 1", cap.count())
	}
	if cap.token != "s3cret" {
		t.Fatalf("token header = %q, want s3cret", cap.token)
	}
	if got := cap.last().Action; got != "payment.create" {
		t.Fatalf("action = %q", got)
	}
}

func TestEmitFailureDoesNotPanic(t *testing.T) {
	c := New("http://127.0.0.1:1", "", "svc", zap.NewNop())
	c.Emit(httptest.NewRequest(http.MethodGet, "/", nil).Context(), Entry{Action: "x"})
}

func TestMiddlewareEmitsOnSuccessOnly(t *testing.T) {
	cap := &captured{}
	srv := cap.server(t)
	defer srv.Close()
	c := New(srv.URL, "tok", "toggle-service", zap.NewNop())

	r := chi.NewRouter()
	r.With(c.Middleware("toggle.update", "feature_toggle", "module", true)).
		Put("/v1/toggles/{module}", func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		})
	r.With(c.Middleware("toggle.update", "feature_toggle", "module", true)).
		Patch("/v1/toggles/{module}", func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusForbidden)
		})

	req := httptest.NewRequest(http.MethodPut, "/v1/toggles/advertising",
		strings.NewReader(`{"enabled":false}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "admin-api/1")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("wrapped status = %d", rec.Code)
	}
	if cap.count() != 1 {
		t.Fatalf("entries = %d, want 1", cap.count())
	}
	e := cap.last()
	if e.EntityID != "advertising" {
		t.Fatalf("entity_id = %q, want advertising", e.EntityID)
	}
	if e.After == nil || !strings.Contains(string(*e.After), `"enabled":false`) {
		t.Fatalf("after body not captured: %v", e.After)
	}
	if e.UA != "admin-api/1" {
		t.Fatalf("ua = %q", e.UA)
	}

	// Failed request (403) must NOT be audited.
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPatch, "/v1/toggles/advertising",
		strings.NewReader(`{"enabled":true}`)))
	if cap.count() != 1 {
		t.Fatalf("entries after 403 = %d, want still 1", cap.count())
	}
}
