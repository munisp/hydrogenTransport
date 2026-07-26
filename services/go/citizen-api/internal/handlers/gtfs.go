package handlers

import (
	"archive/zip"
	"bytes"
	"encoding/csv"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
)

// GTFS static feed (open-data-portal module, SPEC §1 "GTFS/JSON open data";
// BUSINESS_LOGIC_AUDIT §15: the catalog advertised a "GTFS static feed" but
// nothing GTFS-shaped was ever served). The network is frequency-based, so
// the feed is trips.txt + frequencies.txt + stop_times.txt (valid GTFS for
// headway service): one anchor trip per route departing at service start,
// with frequencies covering 05:30–23:00.
// GTFS-RT remains an env-bound residual: no trip-assignment feed exists to
// derive vehicle positions/delays from.

// gtfsFiles renders the five GTFS CSV files from the current network.
func (h *Handler) gtfsFiles(r *http.Request) map[string][]byte {
	stops, routes := h.network(r.Context())
	files := map[string][]byte{}

	// stops.txt
	var sb bytes.Buffer
	sw := csv.NewWriter(&sb)
	_ = sw.Write([]string{"stop_id", "stop_name", "stop_lat", "stop_lon"})
	for _, s := range stops {
		_ = sw.Write([]string{s.StopID, s.StopName,
			strconv.FormatFloat(s.Lat, 'f', 6, 64), strconv.FormatFloat(s.Lon, 'f', 6, 64)})
	}
	sw.Flush()
	files["stops.txt"] = sb.Bytes()

	// routes.txt
	var rb bytes.Buffer
	rw := csv.NewWriter(&rb)
	_ = rw.Write([]string{"route_id", "route_short_name", "route_long_name", "route_type"})
	for _, rt := range routes {
		_ = rw.Write([]string{rt.RouteID, rt.RouteShortName, rt.RouteLongName, "3"})
	}
	rw.Flush()
	files["routes.txt"] = rb.Bytes()

	// trips.txt + frequencies.txt + stop_times.txt
	var tb, fb, stb bytes.Buffer
	tw, fw, stw := csv.NewWriter(&tb), csv.NewWriter(&fb), csv.NewWriter(&stb)
	_ = tw.Write([]string{"route_id", "service_id", "trip_id", "trip_headsign"})
	_ = fw.Write([]string{"trip_id", "start_time", "end_time", "headway_secs"})
	_ = stw.Write([]string{"trip_id", "arrival_time", "departure_time", "stop_id", "stop_sequence"})
	anchor := time.Date(2000, 1, 1, serviceStartHour, serviceStartMin, 0, 0, time.UTC)
	for _, rt := range routes {
		if len(rt.StopIDs) == 0 {
			continue
		}
		tripID := rt.RouteID + "-anchor"
		headsign := ""
		if s, ok := stopByID(stops, rt.StopIDs[len(rt.StopIDs)-1]); ok {
			headsign = s.StopName
		}
		_ = tw.Write([]string{rt.RouteID, "daily", tripID, headsign})
		_ = fw.Write([]string{tripID, "05:30:00", "23:00:00", strconv.Itoa(rt.HeadwayMin * 60)})
		for i, stopID := range rt.StopIDs {
			ts := anchor.Add(time.Duration(i*minutesPerStop) * time.Minute).Format("15:04:05")
			_ = stw.Write([]string{tripID, ts, ts, stopID, strconv.Itoa(i + 1)})
		}
	}
	tw.Flush()
	fw.Flush()
	stw.Flush()
	files["trips.txt"] = tb.Bytes()
	files["frequencies.txt"] = fb.Bytes()
	files["stop_times.txt"] = stb.Bytes()
	return files
}

// GTFSFile handles GET /v1/opendata/gtfs/{file} — one GTFS CSV file
// (stops.txt, routes.txt, trips.txt, stop_times.txt, frequencies.txt).
func (h *Handler) GTFSFile(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "file")
	files := h.gtfsFiles(r)
	content, ok := files[name]
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{
			"error": "unknown GTFS file " + name + " (available: stops.txt, routes.txt, trips.txt, stop_times.txt, frequencies.txt)",
		})
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(content)
}

// GTFSZip handles GET /v1/opendata/gtfs — the full GTFS static feed as one
// zip archive (the artifact the open-data catalog's gtfs-static entry
// advertises).
func (h *Handler) GTFSZip(w http.ResponseWriter, r *http.Request) {
	files := h.gtfsFiles(r)
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, name := range []string{"stops.txt", "routes.txt", "trips.txt", "stop_times.txt", "frequencies.txt"} {
		f, err := zw.Create(name)
		if err != nil {
			h.internal(w, fmt.Sprintf("build GTFS zip entry %s", name), err)
			return
		}
		if _, err := f.Write(files[name]); err != nil {
			h.internal(w, "write GTFS zip entry", err)
			return
		}
	}
	if err := zw.Close(); err != nil {
		h.internal(w, "close GTFS zip", err)
		return
	}
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", `attachment; filename="h2fleet-gtfs.zip"`)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(buf.Bytes())
}
