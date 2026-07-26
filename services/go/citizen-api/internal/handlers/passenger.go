package handlers

import (
	"context"
	"net/http"
	"sort"
	"time"

	"go.uber.org/zap"
)

// GTFS-static network (SPEC §3.4: passenger-pwa serves GTFS-style JSON).
// The network is loaded from fleet.stops / fleet.routes / fleet.route_stops
// (migration 0005 S13; BUSINESS_LOGIC_AUDIT §11: the data was hardcoded Go
// literals and ignored the database). The literal fallback below is only
// used when the database is unavailable or holds no network at all, and is
// identical to the 0005 seed so behaviour is consistent either way. The
// schedule is headway-based: service runs 05:30–23:00, one departure per
// route per headway, stop offsets per route.

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

// fallbackStops / fallbackRoutes mirror the migration 0005 S13 seed exactly;
// they only serve when the DB-backed network cannot be loaded.
var fallbackStops = []Stop{
	{StopID: "S001", StopName: "Central Station", Lat: 52.5200, Lon: 13.4050},
	{StopID: "S002", StopName: "Museum Island", Lat: 52.5169, Lon: 13.4010},
	{StopID: "S003", StopName: "City Hall", Lat: 52.5186, Lon: 13.4081},
	{StopID: "S004", StopName: "Riverside Depot", Lat: 52.5050, Lon: 13.4310},
	{StopID: "S005", StopName: "North H2 Hub", Lat: 52.5400, Lon: 13.3900},
	{StopID: "S006", StopName: "University Campus", Lat: 52.5120, Lon: 13.3260},
	{StopID: "S007", StopName: "Market Square", Lat: 52.5155, Lon: 13.4180},
	{StopID: "S008", StopName: "Stadium Park", Lat: 52.4990, Lon: 13.3890},
}

var fallbackRoutes = []Route{
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

// network loads the stop/route graph from the database; on any failure (no
// DB configured, query error, empty network) it falls back to the seed
// literals so the passenger API never goes blank.
func (h *Handler) network(ctx context.Context) ([]Stop, []Route) {
	if h.db == nil {
		return fallbackStops, fallbackRoutes
	}
	stops, routes, err := h.loadNetwork(ctx)
	if err != nil || len(stops) == 0 || len(routes) == 0 {
		if err != nil {
			h.log.Warn("GTFS network load failed; serving seed fallback", zap.Error(err))
		}
		return fallbackStops, fallbackRoutes
	}
	return stops, routes
}

// loadNetwork reads fleet.stops / fleet.routes / fleet.route_stops into the
// GTFS-style model (stop codes become stop_ids, route codes route_ids).
func (h *Handler) loadNetwork(ctx context.Context) ([]Stop, []Route, error) {
	stopRows, err := h.db.Query(ctx, `
		SELECT code, name, ST_Y(geom), ST_X(geom) FROM fleet.stops ORDER BY code`)
	if err != nil {
		return nil, nil, err
	}
	defer stopRows.Close()
	stops := []Stop{}
	for stopRows.Next() {
		var s Stop
		if err := stopRows.Scan(&s.StopID, &s.StopName, &s.Lat, &s.Lon); err != nil {
			return nil, nil, err
		}
		stops = append(stops, s)
	}
	if err := stopRows.Err(); err != nil {
		return nil, nil, err
	}

	routeRows, err := h.db.Query(ctx, `
		SELECT r.code, r.short_name, r.long_name, COALESCE(r.headway_min, 15),
		       COALESCE(string_agg(s.code, ',' ORDER BY rs.seq), '')
		FROM fleet.routes r
		LEFT JOIN fleet.route_stops rs ON rs.route_id = r.id
		LEFT JOIN fleet.stops s ON s.id = rs.stop_id
		WHERE r.active
		GROUP BY r.id, r.code, r.short_name, r.long_name, r.headway_min
		ORDER BY r.code`)
	if err != nil {
		return nil, nil, err
	}
	defer routeRows.Close()
	routes := []Route{}
	for routeRows.Next() {
		var rt Route
		var stopCSV string
		if err := routeRows.Scan(&rt.RouteID, &rt.RouteShortName, &rt.RouteLongName, &rt.HeadwayMin, &stopCSV); err != nil {
			return nil, nil, err
		}
		rt.RouteType = 3
		if stopCSV != "" {
			rt.StopIDs = splitCSV(stopCSV)
		}
		routes = append(routes, rt)
	}
	if err := routeRows.Err(); err != nil {
		return nil, nil, err
	}
	return stops, routes, nil
}

func splitCSV(csv string) []string {
	out := []string{}
	start := 0
	for i := 0; i <= len(csv); i++ {
		if i == len(csv) || csv[i] == ',' {
			out = append(out, csv[start:i])
			start = i + 1
		}
	}
	return out
}

func stopByID(stops []Stop, id string) (Stop, bool) {
	for _, s := range stops {
		if s.StopID == id {
			return s, true
		}
	}
	return Stop{}, false
}

// Arrival is one upcoming departure at a stop. ScheduleBased is honest
// metadata: there is no GTFS-RT trip-assignment feed in the platform, so
// every arrival is timetable-derived (BUSINESS_LOGIC_AUDIT §11 env-bound
// residual — wire a trip feed to start adjusting for live delays).
type Arrival struct {
	RouteID        string    `json:"route_id"`
	RouteShortName string    `json:"route_short_name"`
	Headsign       string    `json:"headsign"`
	StopID         string    `json:"stop_id"`
	ScheduledAt    time.Time `json:"scheduled_at"`
	InMinutes      int       `json:"in_minutes"`
	ScheduleBased  bool      `json:"schedule_based"`
}

// ListStops handles GET /v1/passenger/stops (GTFS stops.txt style).
func (h *Handler) ListStops(w http.ResponseWriter, r *http.Request) {
	stops, _ := h.network(r.Context())
	writeJSON(w, http.StatusOK, map[string]any{"stops": stops})
}

// ListRoutes handles GET /v1/passenger/routes (GTFS routes.txt style).
func (h *Handler) ListRoutes(w http.ResponseWriter, r *http.Request) {
	_, routes := h.network(r.Context())
	writeJSON(w, http.StatusOK, map[string]any{"routes": routes})
}

// GetArrivals handles GET /v1/passenger/arrivals?stop_id=&limit= — next
// departures for the stop within the service window.
func (h *Handler) GetArrivals(w http.ResponseWriter, r *http.Request) {
	stops, routes := h.network(r.Context())
	stopID := r.URL.Query().Get("stop_id")
	if _, ok := stopByID(stops, stopID); !ok {
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
		arrivals = append(arrivals, nextArrivals(stops, rt, offset, now, 3)...)
	}
	sort.Slice(arrivals, func(i, j int) bool { return arrivals[i].ScheduledAt.Before(arrivals[j].ScheduledAt) })
	if len(arrivals) > 10 {
		arrivals = arrivals[:10]
	}
	writeJSON(w, http.StatusOK, map[string]any{"stop_id": stopID, "generated_at": now.UTC(), "arrivals": arrivals})
}

// nextArrivals computes the next n scheduled departures of route rt at stop
// offset, based on the headway timetable anchored at service start. `after`
// filters departures at/before it (used by the transfer planner so the
// second leg is actually catchable).
func nextArrivalsAfter(stops []Stop, rt Route, stopOffset int, after time.Time, n int) []Arrival {
	out := []Arrival{}
	anchor := time.Date(after.Year(), after.Month(), after.Day(), serviceStartHour, serviceStartMin, 0, 0, after.Location())
	stopTime := anchor.Add(time.Duration(stopOffset*minutesPerStop) * time.Minute)
	headway := time.Duration(rt.HeadwayMin) * time.Minute
	end := time.Date(after.Year(), after.Month(), after.Day(), serviceEndHour, 0, 0, 0, after.Location())

	headsign := ""
	if s, ok := stopByID(stops, rt.StopIDs[len(rt.StopIDs)-1]); ok {
		headsign = s.StopName
	}
	for t := stopTime; t.Before(end) && len(out) < n; t = t.Add(headway) {
		if !t.After(after) { // too early (or just missed) — next one
			continue
		}
		out = append(out, Arrival{
			RouteID:        rt.RouteID,
			RouteShortName: rt.RouteShortName,
			Headsign:       headsign,
			StopID:         rt.StopIDs[stopOffset],
			ScheduledAt:    t.UTC(),
			InMinutes:      int(time.Until(t).Minutes()),
			ScheduleBased:  true,
		})
	}
	return out
}

// nextArrivals computes the next n upcoming departures (from now).
func nextArrivals(stops []Stop, rt Route, stopOffset int, now time.Time, n int) []Arrival {
	return nextArrivalsAfter(stops, rt, stopOffset, now.Add(-time.Minute), n)
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

// JourneyTransfer is a one-transfer option: ride route A to an interchange
// stop shared with route B, then route B to the destination
// (BUSINESS_LOGIC_AUDIT §11: the planner only knew direct rides).
type JourneyTransfer struct {
	TransferStopID string       `json:"transfer_stop_id"`
	Legs           []JourneyLeg `json:"legs"`
	DepartAt       time.Time    `json:"depart_at"`
	ArriveAt       time.Time    `json:"arrive_at"`
	DurationMin    int          `json:"duration_min"`
}

// PlanJourney handles GET /v1/passenger/journey?from=&to= — direct routes
// serving both stops plus one-transfer options through shared interchange
// stops (rule-based fallback planner, SPEC §4).
func (h *Handler) PlanJourney(w http.ResponseWriter, r *http.Request) {
	stops, routes := h.network(r.Context())
	from, to := r.URL.Query().Get("from"), r.URL.Query().Get("to")
	if _, ok := stopByID(stops, from); !ok {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "unknown or missing from stop_id"})
		return
	}
	if _, ok := stopByID(stops, to); !ok {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "unknown or missing to stop_id"})
		return
	}
	direct, transfers := planJourney(stops, routes, from, to, time.Now())
	writeJSON(w, http.StatusOK, map[string]any{
		"from": from, "to": to, "options": direct, "transfers": transfers,
	})
}

// planJourney is the pure planner behind PlanJourney (separated for
// deterministic tests): direct rides plus one-transfer options.
func planJourney(stops []Stop, routes []Route, from, to string, now time.Time) ([]JourneyLeg, []JourneyTransfer) {
	stopIndex := func(rt Route, id string) int {
		for i, sid := range rt.StopIDs {
			if sid == id {
				return i
			}
		}
		return -1
	}
	makeLeg := func(rt Route, fromIdx, toIdx int, after time.Time) *JourneyLeg {
		deps := nextArrivalsAfter(stops, rt, fromIdx, after, 1)
		if len(deps) == 0 {
			return nil
		}
		dep := deps[0].ScheduledAt
		arr := dep.Add(time.Duration((toIdx-fromIdx)*minutesPerStop) * time.Minute)
		return &JourneyLeg{
			RouteID:        rt.RouteID,
			RouteShortName: rt.RouteShortName,
			FromStopID:     rt.StopIDs[fromIdx],
			ToStopID:       rt.StopIDs[toIdx],
			DepartAt:       dep,
			ArriveAt:       arr,
			DurationMin:    int(arr.Sub(dep).Minutes()),
		}
	}

	// Direct rides.
	direct := []JourneyLeg{}
	for _, rt := range routes {
		fromIdx, toIdx := stopIndex(rt, from), stopIndex(rt, to)
		if fromIdx < 0 || toIdx < 0 || fromIdx >= toIdx {
			continue
		}
		if leg := makeLeg(rt, fromIdx, toIdx, now.Add(-time.Minute)); leg != nil {
			direct = append(direct, *leg)
		}
	}
	sort.Slice(direct, func(i, j int) bool { return direct[i].DepartAt.Before(direct[j].DepartAt) })

	// One-transfer options via a shared interchange stop.
	transfers := []JourneyTransfer{}
	seen := map[string]bool{}
	for _, a := range routes {
		fromIdx := stopIndex(a, from)
		if fromIdx < 0 {
			continue
		}
		for _, b := range routes {
			if b.RouteID == a.RouteID {
				continue
			}
			toIdx := stopIndex(b, to)
			if toIdx < 0 {
				continue
			}
			for _, x := range a.StopIDs {
				aX, bX := stopIndex(a, x), stopIndex(b, x)
				if aX <= fromIdx || bX < 0 || bX >= toIdx {
					continue
				}
				key := a.RouteID + "|" + b.RouteID + "|" + x
				if seen[key] {
					continue
				}
				leg1 := makeLeg(a, fromIdx, aX, now.Add(-time.Minute))
				if leg1 == nil {
					continue
				}
				leg2 := makeLeg(b, bX, toIdx, leg1.ArriveAt)
				if leg2 == nil {
					continue
				}
				seen[key] = true
				transfers = append(transfers, JourneyTransfer{
					TransferStopID: x,
					Legs:           []JourneyLeg{*leg1, *leg2},
					DepartAt:       leg1.DepartAt,
					ArriveAt:       leg2.ArriveAt,
					DurationMin:    int(leg2.ArriveAt.Sub(leg1.DepartAt).Minutes()),
				})
			}
		}
	}
	sort.Slice(transfers, func(i, j int) bool { return transfers[i].ArriveAt.Before(transfers[j].ArriveAt) })

	return direct, transfers
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

// serviceAlerts use absolute validity windows so they genuinely expire
// (BUSINESS_LOGIC_AUDIT §11: ActiveUntil was time.Now()+N computed once at
// process start, so alerts never expired).
var serviceAlerts = []ServiceAlert{
	{
		ID:          "AL-001",
		Header:      "Route 42 detour via Market Square",
		Description: "Due to roadworks on Museum Island, route 42 operates via Market Square until the end of September 2026.",
		Severity:    "warning",
		RouteIDs:    []string{"R42"},
		ActiveFrom:  time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC),
		ActiveUntil: time.Date(2026, 9, 30, 23, 59, 59, 0, time.UTC),
	},
	{
		ID:          "AL-002",
		Header:      "Free H2 bus rides on Clean Air Day",
		Description: "All hydrogen bus lines are fare-free on the city Clean Air Day, 2026-09-17.",
		Severity:    "info",
		ActiveFrom:  time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC),
		ActiveUntil: time.Date(2026, 9, 17, 23, 59, 59, 0, time.UTC),
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
