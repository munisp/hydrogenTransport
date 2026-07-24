package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// GET /v1/passenger/arrivals is DB-free (GTFS seed data), so it is exercised
// directly through httptest with a zero-value Handler.
func TestGetArrivals_Shape(t *testing.T) {
	h := &Handler{}
	rec := httptest.NewRecorder()
	h.GetArrivals(rec, httptest.NewRequest(http.MethodGet, "/v1/passenger/arrivals?stop_id=S001", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("got %d, want 200 (body: %s)", rec.Code, rec.Body)
	}
	var body struct {
		StopID      string    `json:"stop_id"`
		GeneratedAt time.Time `json:"generated_at"`
		Arrivals    []Arrival `json:"arrivals"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body.StopID != "S001" {
		t.Fatalf("stop_id echo wrong: %q", body.StopID)
	}
	for i, a := range body.Arrivals {
		if a.StopID != "S001" {
			t.Fatalf("arrival %d for wrong stop: %+v", i, a)
		}
		if a.RouteID == "" || a.RouteShortName == "" || a.Headsign == "" {
			t.Fatalf("arrival %d missing route fields: %+v", i, a)
		}
		if i > 0 && a.ScheduledAt.Before(body.Arrivals[i-1].ScheduledAt) {
			t.Fatalf("arrivals not sorted by scheduled_at: %+v", body.Arrivals)
		}
	}
}

// Unknown or missing stop_id must be a 400, not an empty 200.
func TestGetArrivals_UnknownStop(t *testing.T) {
	h := &Handler{}
	for _, url := range []string{
		"/v1/passenger/arrivals?stop_id=NOPE",
		"/v1/passenger/arrivals",
	} {
		rec := httptest.NewRecorder()
		h.GetArrivals(rec, httptest.NewRequest(http.MethodGet, url, nil))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: got %d, want 400", url, rec.Code)
		}
	}
}

// nextArrivals is the schedule engine behind the endpoint: table tests pin
// the service-window behaviour deterministically (independent of wall clock).
func TestNextArrivals(t *testing.T) {
	r10 := routes[0] // R10, headway 10min, S001 at offset 0
	if r10.RouteID != "R10" {
		t.Fatalf("seed data changed: routes[0] = %+v", r10)
	}
	loc := time.UTC
	at := func(hh, mm int) time.Time { return time.Date(2026, 7, 24, hh, mm, 0, 0, loc) }

	cases := []struct {
		name       string
		now        time.Time
		stopOffset int
		wantFirst  time.Time
		wantCount  int
	}{
		// Mid-morning: next departures follow the 10-minute headway.
		{"midday offset0", at(10, 3), 0, at(10, 10), 3},
		// Stop offset shifts the timetable by minutesPerStop per stop.
		{"midday offset2", at(10, 3), 2, at(10, 8), 3},
		// Before the 05:30 service start the first departure is the anchor.
		{"before window", at(4, 0), 0, at(5, 30), 3},
		// After the 23:00 service end there are no more departures today.
		{"after window", at(23, 30), 0, time.Time{}, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := nextArrivals(r10, tc.stopOffset, tc.now, 3)
			if len(got) != tc.wantCount {
				t.Fatalf("got %d arrivals, want %d (%+v)", len(got), tc.wantCount, got)
			}
			if tc.wantCount == 0 {
				return
			}
			if !got[0].ScheduledAt.Equal(tc.wantFirst) {
				t.Fatalf("first departure %v, want %v", got[0].ScheduledAt, tc.wantFirst)
			}
			headway := time.Duration(r10.HeadwayMin) * time.Minute
			for i := 1; i < len(got); i++ {
				if d := got[i].ScheduledAt.Sub(got[i-1].ScheduledAt); d != headway {
					t.Fatalf("headway %v, want %v", d, headway)
				}
			}
		})
	}
}

// Stops and routes seed lists must stay non-empty (PWA depends on them).
func TestSeedDataNonEmpty(t *testing.T) {
	if len(stops) == 0 || len(routes) == 0 {
		t.Fatal("GTFS seed data must not be empty")
	}
	for _, rt := range routes {
		for _, id := range rt.StopIDs {
			if _, ok := stopByID(id); !ok {
				t.Fatalf("route %s references unknown stop %s", rt.RouteID, id)
			}
		}
	}
}
