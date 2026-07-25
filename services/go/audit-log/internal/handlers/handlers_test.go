package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/munisp/hydrogenTransport/services/go/audit-log/internal/anomaly"
	"github.com/munisp/hydrogenTransport/services/go/audit-log/internal/mirror"
	"github.com/munisp/hydrogenTransport/services/go/audit-log/internal/store"
)

// fakeStore is an in-memory store.Store implementing the same chain logic.
type fakeStore struct {
	mu      sync.Mutex
	entries []store.Entry
}

func (f *fakeStore) EnsureSchema(context.Context) error { return nil }
func (f *fakeStore) Ping(context.Context) error         { return nil }

func (f *fakeStore) Append(_ context.Context, e *store.Entry) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	prev := ""
	if n := len(f.entries); n > 0 {
		prev = f.entries[n-1].Hash
	}
	e.ID = int64(len(f.entries) + 1)
	if e.TS.IsZero() {
		e.TS = time.Now().UTC()
	}
	e.PrevHash = prev
	e.Hash = store.ChainHash(prev, *e)
	f.entries = append(f.entries, *e)
	return nil
}

func (f *fakeStore) List(_ context.Context, filter store.ListFilter) ([]store.Entry, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []store.Entry{}
	for i := len(f.entries) - 1; i >= 0; i-- {
		e := f.entries[i]
		if filter.Actor != "" && e.ActorSub != filter.Actor {
			continue
		}
		if filter.Entity != "" && e.Entity != filter.Entity {
			continue
		}
		if !filter.From.IsZero() && e.TS.Before(filter.From) {
			continue
		}
		out = append(out, e)
	}
	return out, nil
}

func (f *fakeStore) Verify(context.Context) (int64, int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	prev := ""
	for _, e := range f.entries {
		if e.PrevHash != prev || store.ChainHash(prev, e) != e.Hash {
			return e.ID, int(e.ID), nil
		}
		prev = e.Hash
	}
	return 0, len(f.entries), nil
}

func newTestHandler(st store.Store) *Handler {
	return New(st, mirror.New("", "", zap.NewNop()),
		anomaly.New(1000, time.Minute, "", zap.NewNop()), zap.NewNop())
}

func doRequest(h http.HandlerFunc, method, target, body string, headers map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h(rec, req)
	return rec
}

func TestIngestValidatesRequiredFields(t *testing.T) {
	h := newTestHandler(&fakeStore{})
	for _, body := range []string{
		`{}`,
		`{"actor_sub":"a"}`,
		`{"actor_sub":"a","action":"x"}`,
		`{"action":"x","entity":"y"}`,
	} {
		rec := doRequest(h.Ingest, http.MethodPost, "/v1/audit", body, nil)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("body %s: status = %d, want 400", body, rec.Code)
		}
	}
}

func TestIngestAppendsAndChainsEntries(t *testing.T) {
	st := &fakeStore{}
	h := newTestHandler(st)

	rec := doRequest(h.Ingest, http.MethodPost, "/v1/audit",
		`{"actor_sub":"svc-commerce","action":"payment.create","entity":"fare_payment","entity_id":"p1","after":{"amount":250},"ip":"10.1.1.1","ua":"commerce-api/1"}`, nil)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (%s)", rec.Code, rec.Body.String())
	}
	var e1 store.Entry
	if err := json.Unmarshal(rec.Body.Bytes(), &e1); err != nil {
		t.Fatal(err)
	}
	if e1.ID != 1 || e1.PrevHash != "" || len(e1.Hash) != 64 {
		t.Fatalf("genesis entry malformed: %+v", e1)
	}

	rec = doRequest(h.Ingest, http.MethodPost, "/v1/audit",
		`{"actor_sub":"svc-toggle","action":"toggle.update","entity":"feature_toggle","entity_id":"advertising"}`, nil)
	var e2 store.Entry
	_ = json.Unmarshal(rec.Body.Bytes(), &e2)
	if e2.PrevHash != e1.Hash {
		t.Fatalf("chain broken: e2.prev=%s e1.hash=%s", e2.PrevHash, e1.Hash)
	}

	entries, _ := st.List(context.Background(), store.ListFilter{})
	if len(entries) != 2 {
		t.Fatalf("list count = %d, want 2", len(entries))
	}
}

func TestListFilters(t *testing.T) {
	st := &fakeStore{}
	h := newTestHandler(st)
	mustAppend := func(actor, entity string) {
		t.Helper()
		e := store.Entry{ActorSub: actor, Action: "x", Entity: entity, TS: time.Now().UTC()}
		if err := st.Append(context.Background(), &e); err != nil {
			t.Fatal(err)
		}
	}
	mustAppend("alice", "user")
	mustAppend("alice", "feature_toggle")
	mustAppend("bob", "user")

	rec := doRequest(h.List, http.MethodGet, "/v1/audit?actor=alice", "", nil)
	var resp struct {
		Entries []store.Entry `json:"entries"`
		Count   int           `json:"count"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp.Count != 2 {
		t.Fatalf("actor filter count = %d, want 2", resp.Count)
	}

	rec = doRequest(h.List, http.MethodGet, "/v1/audit?actor=alice&entity=user", "", nil)
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp.Count != 1 {
		t.Fatalf("combined filter count = %d, want 1", resp.Count)
	}

	rec = doRequest(h.List, http.MethodGet, "/v1/audit?from=not-a-time", "", nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("bad from: status = %d, want 400", rec.Code)
	}
}

func TestVerifyEndpoint(t *testing.T) {
	st := &fakeStore{}
	h := newTestHandler(st)
	e := store.Entry{ActorSub: "a", Action: "x", Entity: "y"}
	if err := st.Append(context.Background(), &e); err != nil {
		t.Fatal(err)
	}

	rec := doRequest(h.Verify, http.MethodGet, "/v1/audit/verify", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var resp struct {
		OK      bool  `json:"ok"`
		Checked int   `json:"checked"`
		BadID   int64 `json:"first_bad_id"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if !resp.OK || resp.Checked != 1 || resp.BadID != 0 {
		t.Fatalf("unexpected verify response: %+v", resp)
	}

	// Tamper directly with the stored entry.
	st.mu.Lock()
	st.entries[0].ActorSub = "mallory"
	st.mu.Unlock()
	rec = doRequest(h.Verify, http.MethodGet, "/v1/audit/verify", "", nil)
	if rec.Code != http.StatusConflict {
		t.Fatalf("tampered chain: status = %d, want 409", rec.Code)
	}
}

func TestRequireIngestAuthTokenPath(t *testing.T) {
	called := false
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusCreated)
	})
	jwtFallback := func(h http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
		})
	}
	mw := RequireIngestAuth("s3cret", jwtFallback)

	rec := httptest.NewRecorder()
	mw(next).ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/audit",
		strings.NewReader(`{}`)))
	if rec.Code != http.StatusUnauthorized || called {
		t.Fatal("request without token must fall through to JWT (401)")
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/audit", strings.NewReader(`{}`))
	req.Header.Set("X-Audit-Token", "s3cret")
	rec = httptest.NewRecorder()
	mw(next).ServeHTTP(rec, req)
	if !called || rec.Code != http.StatusCreated {
		t.Fatal("valid token must pass without JWT")
	}

	req = httptest.NewRequest(http.MethodPost, "/v1/audit", strings.NewReader(`{}`))
	req.Header.Set("X-Audit-Token", "wrong")
	rec = httptest.NewRecorder()
	called = false
	mw(next).ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized || called {
		t.Fatal("wrong token must fall through to JWT (401)")
	}
}

func TestHealthz(t *testing.T) {
	h := newTestHandler(&fakeStore{})
	rec := doRequest(h.Healthz, http.MethodGet, "/healthz", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
}
