package handlers

import (
	"net/http"
	"sort"
	"time"
)

// GTFS-static style seed dataset (SPEC §3.4: passenger-pwa serves GTFS-style
// JSON from seed data). The schedule is headway-based: service runs 05:30–23:00,
// one departure per route per headway, stop offsets per route.

// Stop is a GTFS stop.
type Stop struct {
	StopID   string  `json:"stop_id"`
	StopName string  `json:"stop_name"`
	Lat      float64 `json:"stop_lat"`
	Lon      float64 `json:"stop_lon"`
}

// Route is a GTFS route with its ordered stop sequence.
type Route struct {
	RouteID        string   `json:"route_id"`
	RouteShortName string   `json:"route_short_name"`
	RouteLongName  string   `json:"route_long_name"`
	RouteType      int      `json:"route_type"` // 3 = bus
	HeadwayMin     int      `json:"headway_min"`
	StopIDs        []string `json:"stop_ids"`
}

var stops = []Stop{
	{StopID: "S001", StopName: "Central Station", Lat: 52.5200, Lon: 13.4050},
	{StopID: "S002", StopName: "Museum Island", Lat: 52.5169, Lon: 13.4010},
	{StopID: "S003", StopName: "City Hall", Lat: 52.5186, Lon: 13.4081},
	{StopID: "S004", StopName: "Riverside Depot", Lat: 52.5050, Lon: 13.4310},
	{StopID: "S005", StopName: "North H2 Hub", Lat: 52.5400, Lon: 13.3900},
	{StopID: "S006", StopName: "University Campus", Lat: 52.5120, Lon: 13.3260},
	{StopID: "S007", StopName: "Market Square", Lat: 52.5155, Lon: 13.4180},
	{StopID: "S008", StopName: "Stadium Park", Lat: 52.4990, Lon: 13.3890},
}

var routes = []Route{
	{RouteID: "R10", RouteShortName: "10", RouteLongName: "Central Station — North H2 Hub", RouteType: 3, HeadwayMin: 10,
		StopIDs: []string{"S001", "S002", "S003", "S005"}},
	{RouteID: "R21", RouteShortName: "21", RouteLongName: "University — Riverside Depot", RouteType: 3, HeadwayMin: 15,
		StopIDs: []string{"S006", "S001", "S007", "S004"}},
	{RouteID: "R42", RouteShortName: "42", RouteLongName: "Stadium Park — City Hall", RouteType: 3, HeadwayMin: 20,
		StopIDs: []string{"S008", "S007", "S003", "S001"}},
}

const (
	serviceStartHour = 5
	serviceStartMin  = 30
	serviceEndHour   = 23
	minutesPerStop   = 4 // average inter-stop travel time
)

func stopByID(id string) (Stop, bool) {
	for _, s := range stops {
		if s.StopID == id {
			return s, true
		}
	}
	return Stop{}, false
}

// Arrival is one upcoming departure at a stop.
type Arrival struct {
	RouteID        string    `json:"route_id"`
	RouteShortName string    `json:"route_short_name"`
	Headsign       string    `json:"headsign"`
	StopID         string    `json:"stop_id"`
	ScheduledAt    time.Time `json:"scheduled_at"`
	InMinutes      int       `json:"in_minutes"`
}

// ListStops handles GET /v1/passenger/stops (GTFS stops.txt style).
func (h *Handler) ListStops(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"stops": stops})
}

// ListRoutes handles GET /v1/passenger/routes (GTFS routes.txt style).
func (h *Handler) ListRoutes(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"routes": routes})
}

// GetArrivals handles GET /v1/passenger/arrivals?stop_id=&limit= — next
// departures for the stop within the service window.
func (h *Handler) GetArrivals(w http.ResponseWriter, r *http.Request) {
	stopID := r.URL.Query().Get("stop_id")
	if _, ok := stopByID(stopID); !ok {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "unknown or missing stop_id"})
		return
	}
	now := time.Now()
	arrivals := []Arrival{}
	for _, rt := range routes {
		offset := -1
		for i, id := range rt.StopIDs {
			if id == stopID {
				offset = i
				break
			}
		}
		if offset < 0 {
			continue
		}
		arrivals = append(arrivals, nextArrivals(rt, offset, now, 3)...)
	}
	sort.Slice(arrivals, func(i, j int) bool { return arrivals[i].ScheduledAt.Before(arrivals[j].ScheduledAt) })
	if len(arrivals) > 10 {
		arrivals = arrivals[:10]
	}
	writeJSON(w, http.StatusOK, map[string]any{"stop_id": stopID, "generated_at": now.UTC(), "arrivals": arrivals})
}

// nextArrivals computes the next n scheduled departures of route rt at stop
// offset, based on the headway timetable anchored at service start.
func nextArrivals(rt Route, stopOffset int, now time.Time, n int) []Arrival {
	out := []Arrival{}
	anchor := time.Date(now.Year(), now.Month(), now.Day(), serviceStartHour, serviceStartMin, 0, 0, now.Location())
	stopTime := anchor.Add(time.Duration(stopOffset*minutesPerStop) * time.Minute)
	headway := time.Duration(rt.HeadwayMin) * time.Minute
	end := time.Date(now.Year(), now.Month(), now.Day(), serviceEndHour, 0, 0, 0, now.Location())

	headsign := ""
	if s, ok := stopByID(rt.StopIDs[len(rt.StopIDs)-1]); ok {
		headsign = s.StopName
	}
	for t := stopTime; t.Before(end) && len(out) < n; t = t.Add(headway) {
		if t.Before(now.Add(-time.Minute)) { // just missed it; keep schedule tidy
			continue
		}
		out = append(out, Arrival{
			RouteID:        rt.RouteID,
			RouteShortName: rt.RouteShortName,
			Headsign:       headsign,
			StopID:         rt.StopIDs[stopOffset],
			ScheduledAt:    t.UTC(),
			InMinutes:      int(time.Until(t).Minutes()),
		})
	}
	return out
}

// JourneyLeg is one direct-ride option between two stops.
type JourneyLeg struct {
	RouteID        string    `json:"route_id"`
	RouteShortName string    `json:"route_short_name"`
	FromStopID     string    `json:"from_stop_id"`
	ToStopID       string    `json:"to_stop_id"`
	DepartAt       time.Time `json:"depart_at"`
	ArriveAt       time.Time `json:"arrive_at"`
	DurationMin    int       `json:"duration_min"`
}

// PlanJourney handles GET /v1/passenger/journey?from=&to= — direct routes
// serving both stops (rule-based fallback planner, SPEC §4).
func (h *Handler) PlanJourney(w http.ResponseWriter, r *http.Request) {
	from, to := r.URL.Query().Get("from"), r.URL.Query().Get("to")
	if _, ok := stopByID(from); !ok {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "unknown or missing from stop_id"})
		return
	}
	if _, ok := stopByID(to); !ok {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "unknown or missing to stop_id"})
		return
	}
	now := time.Now()
	legs := []JourneyLeg{}
	for _, rt := range routes {
		fromIdx, toIdx := -1, -1
		for i, id := range rt.StopIDs {
			if id == from {
				fromIdx = i
			}
			if id == to {
				toIdx = i
			}
		}
		if fromIdx < 0 || toIdx < 0 || fromIdx >= toIdx {
			continue
		}
		deps := nextArrivals(rt, fromIdx, now, 1)
		if len(deps) == 0 {
			continue
		}
		dep := deps[0].ScheduledAt
		arr := dep.Add(time.Duration((toIdx-fromIdx)*minutesPerStop) * time.Minute)
		legs = append(legs, JourneyLeg{
			RouteID:        rt.RouteID,
			RouteShortName: rt.RouteShortName,
			FromStopID:     from,
			ToStopID:       to,
			DepartAt:       dep,
			ArriveAt:       arr,
			DurationMin:    int(arr.Sub(dep).Minutes()),
		})
	}
	sort.Slice(legs, func(i, j int) bool { return legs[i].DepartAt.Before(legs[j].DepartAt) })
	writeJSON(w, http.StatusOK, map[string]any{"from": from, "to": to, "options": legs})
}

// ServiceAlert is a GTFS-RT style service alert.
type ServiceAlert struct {
	ID          string    `json:"id"`
	Header      string    `json:"header"`
	Description string    `json:"description"`
	Severity    string    `json:"severity"` // info|warning|severe
	RouteIDs    []string  `json:"route_ids,omitempty"`
	ActiveFrom  time.Time `json:"active_from"`
	ActiveUntil time.Time `json:"active_until"`
}

var serviceAlerts = []ServiceAlert{
	{
		ID:          "AL-001",
		Header:      "Route 42 detour via Market Square",
		Description: "Due to roadworks on Museum Island, route 42 operates via Market Square until further notice.",
		Severity:    "warning",
		RouteIDs:    []string{"R42"},
		ActiveFrom:  time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC),
		ActiveUntil: time.Now().Add(30 * 24 * time.Hour),
	},
	{
		ID:          "AL-002",
		Header:      "Free H2 bus rides on Clean Air Day",
		Description: "All hydrogen bus lines are fare-free on the city Clean Air Day.",
		Severity:    "info",
		ActiveFrom:  time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC),
		ActiveUntil: time.Now().Add(60 * 24 * time.Hour),
	},
}

// ListAlerts handles GET /v1/passenger/alerts — currently active service alerts.
func (h *Handler) ListAlerts(w http.ResponseWriter, r *http.Request) {
	now := time.Now()
	active := []ServiceAlert{}
	for _, a := range serviceAlerts {
		if now.After(a.ActiveFrom) && now.Before(a.ActiveUntil) {
			active = append(active, a)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"alerts": active})
}

// MobileConfig handles GET /v1/mobile/config — bootstrap config for the Expo
// citizen/driver apps (mobile-app module), including the citizen-domain module
// toggle states so the app can hide disabled features.
func (h *Handler) MobileConfig(w http.ResponseWriter, r *http.Request) {
	modules := map[string]bool{}
	for _, m := range []string{"passenger-pwa", "mobile-app", "demand-responsive", "carbon-credits", "open-data-portal"} {
		modules[m] = h.tc.IsEnabled(r.Context(), m)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"api_version": "v1",
		"modules":     modules,
	})
}
