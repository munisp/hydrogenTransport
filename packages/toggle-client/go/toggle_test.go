package toggle

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestIsEnabledEnabled(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/toggles/telematics" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(toggleResponse{Module: "telematics", Enabled: true, Domain: "fleet"})
	}))
	defer srv.Close()

	c := New(srv.URL)
	if !c.IsEnabled(context.Background(), "telematics") {
		t.Fatal("expected telematics to be enabled")
	}
}

func TestIsEnabledFailClosedOnServerDown(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	srv.Close() // immediately unreachable

	c := New(srv.URL)
	if c.IsEnabled(context.Background(), "telematics") {
		t.Fatal("expected fail-closed (false) when toggle-service is unreachable")
	}
}

func TestIsEnabledFailClosedOnNon200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	c := New(srv.URL)
	if c.IsEnabled(context.Background(), "digital-twin") {
		t.Fatal("expected fail-closed (false) on non-200 response")
	}
}

func TestIsEnabledCachesForTTL(t *testing.T) {
	var calls int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&calls, 1)
		_ = json.NewEncoder(w).Encode(toggleResponse{Module: "fuel-monitoring", Enabled: true, Domain: "fleet"})
	}))
	defer srv.Close()

	c := New(srv.URL)
	for i := 0; i < 5; i++ {
		if !c.IsEnabled(context.Background(), "fuel-monitoring") {
			t.Fatal("expected enabled")
		}
	}
	if got := atomic.LoadInt64(&calls); got != 1 {
		t.Fatalf("expected 1 upstream call due to caching, got %d", got)
	}
}

func TestIsEnabledCacheExpires(t *testing.T) {
	var calls int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&calls, 1)
		_ = json.NewEncoder(w).Encode(toggleResponse{Module: "m", Enabled: true, Domain: "fleet"})
	}))
	defer srv.Close()

	c := New(srv.URL)
	c.ttl = 20 * time.Millisecond // shorten for the test

	c.IsEnabled(context.Background(), "m")
	time.Sleep(40 * time.Millisecond)
	c.IsEnabled(context.Background(), "m")

	if got := atomic.LoadInt64(&calls); got != 2 {
		t.Fatalf("expected 2 upstream calls after TTL expiry, got %d", got)
	}
}

func TestInvalidate(t *testing.T) {
	var calls int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&calls, 1)
		_ = json.NewEncoder(w).Encode(toggleResponse{Module: "m", Enabled: true, Domain: "fleet"})
	}))
	defer srv.Close()

	c := New(srv.URL)
	c.IsEnabled(context.Background(), "m")
	c.Invalidate("m")
	c.IsEnabled(context.Background(), "m")

	if got := atomic.LoadInt64(&calls); got != 2 {
		t.Fatalf("expected 2 upstream calls after Invalidate, got %d", got)
	}
}
