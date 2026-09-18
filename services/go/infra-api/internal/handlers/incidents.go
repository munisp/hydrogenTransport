package handlers

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"go.uber.org/zap"

	auth "github.com/munisp/hydrogenTransport/packages/go-auth"
)

// Incident mirrors infra.incidents.
type Incident struct {
	ID        string         `json:"id"`
	Type      string         `json:"type"`
	Severity  string         `json:"severity"`
	BusID     *string        `json:"bus_id,omitempty"`
	StationID *string        `json:"station_id,omitempty"`
	Status    string         `json:"status"`
	OpenedAt  time.Time      `json:"opened_at"`
	Meta      map[string]any `json:"meta,omitempty"`
}

const incidentCols = `id, type, COALESCE(severity,'medium'), bus_id, station_id,
	COALESCE(status,'open'), opened_at, COALESCE(meta,'{}'::jsonb)`

func scanIncident(row pgx.Row) (Incident, error) {
	var i Incident
	var meta []byte
	err := row.Scan(&i.ID, &i.Type, &i.Severity, &i.BusID, &i.StationID, &i.Status, &i.OpenedAt, &meta)
	if err == nil && len(meta) > 0 {
		_ = json.Unmarshal(meta, &i.Meta)
	}
	return i, err
}

// ListIncidents handles GET /v1/incidents?status= (leak-detection module).
func (h *Handler) ListIncidents(w http.ResponseWriter, r *http.Request) {
	query := `SELECT ` + incidentCols + ` FROM infra.incidents`
	args := []any{}
	if status := r.URL.Query().Get("status"); status != "" {
		query += ` WHERE status = $1`
		args = append(args, status)
	}
	query += ` ORDER BY opened_at DESC LIMIT 200`

	rows, err := h.db.Query(r.Context(), query, args...)
	if err != nil {
		h.internal(w, "list incidents", err)
		return
	}
	defer rows.Close()

	incidents := []Incident{}
	for rows.Next() {
		i, err := scanIncident(rows)
		if err != nil {
			h.internal(w, "scan incident", err)
			return
		}
		incidents = append(incidents, i)
	}
	if err := rows.Err(); err != nil {
		h.internal(w, "iterate incidents", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"incidents": incidents})
}

type createIncidentRequest struct {
	Type      string  `json:"type"`
	Severity  string  `json:"severity"`
	BusID     *string `json:"bus_id"`
	StationID *string `json:"station_id"`
	// Description is free-text reporter context (mobile DriverScreen and the
	// PWA SafetyPage send it; it was previously dropped — BUSINESS_LOGIC_AUDIT
	// §12). Persisted as meta.description so operators actually see it.
	Description string         `json:"description"`
	Meta        map[string]any `json:"meta"`
}

// incidentTypeFloor is the incident type enum with its minimum severity
// (Wave-6 A4-02): a collision cannot be filed as "low", an SOS is always
// critical. Unknown types are rejected so a typo can never silently bypass
// every escalation rule that matches on type.
var incidentTypeFloor = map[string]string{
	"h2_leak":    "low",
	"collision":  "high",
	"fire":       "critical",
	"prd_vent":   "high",
	"evacuation": "high",
	"sos":        "critical",
	"security":   "high",
	"breakdown":  "low",
	"other":      "low",
}

// applySeverityFloor raises sev to the type's minimum (never lowers a
// caller's escalation) and normalizes unknown severities to medium.
func applySeverityFloor(incidentType, sev string) string {
	if _, ok := severityRank[sev]; !ok {
		sev = "medium"
	}
	if floor, ok := incidentTypeFloor[incidentType]; ok && severityRank[floor] > severityRank[sev] {
		return floor
	}
	return sev
}

// OpenIncident handles POST /v1/incidents (Keycloak JWT).
func (h *Handler) OpenIncident(w http.ResponseWriter, r *http.Request) {
	var req createIncidentRequest
	if err := decodeJSON(w, r, &req); err != nil || req.Type == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "body must include \"type\""})
		return
	}
	if _, ok := incidentTypeFloor[req.Type]; !ok {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{
			"error": "unknown incident type " + strconv.Quote(req.Type) +
				" (allowed: collision, evacuation, fire, h2_leak, breakdown, other, prd_vent, security, sos)"})
		return
	}
	if req.Severity == "" {
		req.Severity = "medium"
	}
	req.Severity = applySeverityFloor(req.Type, req.Severity)
	if req.Description != "" {
		if req.Meta == nil {
			req.Meta = map[string]any{}
		}
		req.Meta["description"] = req.Description
	}
	i, err := h.insertIncident(r, req)
	if err != nil {
		h.internal(w, "open incident", err)
		return
	}
	h.afterIncidentOpened(r, i)
	writeJSON(w, http.StatusCreated, i)
}

// afterIncidentOpened runs the Wave-6 post-creation effects shared by every
// incident ingress path (manual open, leak ingest, driver SOS):
// station emergency on critical station incidents (A4-06) and outbound
// partner webhooks (A3-03).
func (h *Handler) afterIncidentOpened(r *http.Request, i Incident) {
	h.setStationEmergency(r, i)
	h.dispatchIncidentWebhooks(r.Context(), i, "incident.opened")
}

// setStationEmergency flips a station to 'emergency' when a critical
// incident opens against it (Wave-6 A4-06). The station queue already
// rejects joins for any non-online station, so this one transition also
// stops further buses from queueing into a venting station.
func (h *Handler) setStationEmergency(r *http.Request, i Incident) {
	if i.Severity != "critical" || i.StationID == nil {
		return
	}
	var flipped string
	err := h.db.QueryRow(r.Context(), `
		UPDATE infra.stations SET status = 'emergency'
		WHERE id = $1 AND status != 'emergency'
		RETURNING id`, *i.StationID).Scan(&flipped)
	if errors.Is(err, pgx.ErrNoRows) {
		return // already emergency (or unknown station)
	}
	if err != nil {
		h.log.Error("failed to set station emergency", zap.String("station", *i.StationID), zap.Error(err))
		return
	}
	if err := h.pub.Publish(r.Context(), "station.status.changed", map[string]any{
		"station_id": *i.StationID,
		"status":     "emergency",
		"reason":     "critical incident " + i.ID,
	}); err != nil {
		h.log.Error("failed to publish station.status.changed", zap.Error(err))
	}
}

// maybeRestoreStation flips a station back to 'online' when its last open
// critical incident resolves (Wave-6 A4-06).
func (h *Handler) maybeRestoreStation(r *http.Request, stationID string) {
	var flipped string
	err := h.db.QueryRow(r.Context(), `
		UPDATE infra.stations SET status = 'online'
		WHERE id = $1 AND status = 'emergency'
		  AND NOT EXISTS (
			SELECT 1 FROM infra.incidents
			WHERE station_id = $1 AND severity = 'critical'
			  AND status IN ('open','in_progress','acknowledged'))
		RETURNING id`, stationID).Scan(&flipped)
	if errors.Is(err, pgx.ErrNoRows) {
		return
	}
	if err != nil {
		h.log.Error("failed to restore station", zap.String("station", stationID), zap.Error(err))
		return
	}
	if err := h.pub.Publish(r.Context(), "station.status.changed", map[string]any{
		"station_id": stationID,
		"status":     "online",
		"reason":     "all critical incidents resolved",
	}); err != nil {
		h.log.Error("failed to publish station.status.changed", zap.Error(err))
	}
}

func (h *Handler) insertIncident(r *http.Request, req createIncidentRequest) (Incident, error) {
	metaJSON, _ := json.Marshal(req.Meta)
	if req.Meta == nil {
		metaJSON = []byte(`{}`)
	}
	return scanIncident(h.db.QueryRow(r.Context(), `
		INSERT INTO infra.incidents (type, severity, bus_id, station_id, status, meta)
		VALUES ($1, $2, $3, $4, 'open', $5::jsonb)
		RETURNING `+incidentCols,
		req.Type, req.Severity, req.BusID, req.StationID, string(metaJSON)))
}

// AckIncident handles POST /v1/incidents/{id}/ack (Keycloak JWT). Incidents
// set in_progress by the incident-response workflow can be acknowledged too.
func (h *Handler) AckIncident(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	i, err := scanIncident(h.db.QueryRow(r.Context(), `
		UPDATE infra.incidents SET status = 'acknowledged'
		WHERE id = $1 AND status IN ('open','in_progress')
		RETURNING `+incidentCols, id))
	if errors.Is(err, pgx.ErrNoRows) {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "incident not found or not in an acknowledgeable status"})
		return
	}
	if err != nil {
		h.internal(w, "ack incident", err)
		return
	}
	// Signal the workflow so the escalation timer is cancelled.
	if err := h.wf.Signal(r.Context(), "incident-"+id, "incident-acknowledged",
		map[string]any{"incident_id": id}); err != nil {
		h.log.Error("failed to signal incident acknowledgement", zap.String("incident", id), zap.Error(err))
	}
	writeJSON(w, http.StatusOK, i)
}

// ResolveIncident handles POST /v1/incidents/{id}/resolve (Keycloak JWT).
func (h *Handler) ResolveIncident(w http.ResponseWriter, r *http.Request) {
	// Allow resolving open, in_progress and acknowledged incidents.
	id := chi.URLParam(r, "id")
	i, err := scanIncident(h.db.QueryRow(r.Context(), `
		UPDATE infra.incidents SET status = 'resolved', resolved_at = now()
		WHERE id = $1 AND status IN ('open','in_progress','acknowledged')
		RETURNING `+incidentCols, id))
	if errors.Is(err, pgx.ErrNoRows) {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "incident not found or already resolved"})
		return
	}
	if err != nil {
		h.internal(w, "resolve incident", err)
		return
	}
	// Signal the workflow so it closes.
	if err := h.wf.Signal(r.Context(), "incident-"+id, "incident-resolved",
		map[string]any{"incident_id": id}); err != nil {
		h.log.Error("failed to signal incident resolution", zap.String("incident", id), zap.Error(err))
	}
	// Wave-6: restore the station when its last critical incident resolves
	// (A4-06) and notify partner webhooks (A3-03).
	if i.StationID != nil {
		h.maybeRestoreStation(r, *i.StationID)
	}
	h.dispatchIncidentWebhooks(r.Context(), i, "incident.resolved")
	writeJSON(w, http.StatusOK, i)
}

type leakEventRequest struct {
	SensorID  string   `json:"sensor_id"`
	StationID *string  `json:"station_id"`
	BusID     *string  `json:"bus_id"`
	Severity  string   `json:"severity"`
	H2Ppm     *float64 `json:"h2_ppm"`
	Location  string   `json:"location"`
}

// severityRank orders the incident severity enum (must match the escalation
// order in workflow/activities.go).
var severityRank = map[string]int{"low": 0, "medium": 1, "high": 2, "critical": 3}

// severityFromPpm maps an H2 concentration reading to an incident severity
// (BUSINESS_LOGIC_AUDIT §7: ppm was stored but never used). Bands are
// anchored to the H2 lower explosive limit (LEL ≈ 4%vol = 40,000 ppm):
//
//	< 1,000 ppm          low      (trace / sensor noise band)
//	1,000 – 5,000 ppm    medium   (2.5–12.5% LEL — investigate)
//	5,000 – 20,000 ppm   high     (12.5–50% LEL — act now)
//	>= 20,000 ppm        critical (>= 50% LEL — immediate danger)
func severityFromPpm(ppm float64) string {
	switch {
	case ppm >= 20000:
		return "critical"
	case ppm >= 5000:
		return "high"
	case ppm >= 1000:
		return "medium"
	default:
		return "low"
	}
}

// leakSeverity derives the effective severity: the caller's value is never
// trusted blindly — when h2_ppm is present the ppm-derived band RAISES the
// floor (a sensor reading 30,000 ppm cannot be filed as "low"), while a
// caller may still escalate above the band.
func leakSeverity(caller string, ppm *float64) string {
	if caller == "" {
		caller = "high" // documented default when neither is supplied
	}
	if _, ok := severityRank[caller]; !ok {
		caller = "high"
	}
	if ppm == nil {
		return caller
	}
	derived := severityFromPpm(*ppm)
	if severityRank[derived] > severityRank[caller] {
		return derived
	}
	return caller
}

// leakDedupWindow bounds sensor flapping: a repeat reading from the same
// sensor while an earlier leak incident is still active does not open a
// second incident (BUSINESS_LOGIC_AUDIT §7: no dedup).
const leakDedupWindow = "30 minutes"

// IngestLeak handles POST /v1/safety/leak — the H2 sensor ingestion webhook
// (leak-detection module; authenticated by sensor token or JWT at the router).
// It opens an incident, publishes safety.leak.detected (SPEC §3.3) and signals
// the Temporal incident-response workflow. Repeat readings from the same
// sensor within the dedup window are folded into the still-active incident
// (200 + deduplicated:true) instead of flooding infra.incidents.
func (h *Handler) IngestLeak(w http.ResponseWriter, r *http.Request) {
	var req leakEventRequest
	if err := decodeJSON(w, r, &req); err != nil || req.SensorID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "body must include \"sensor_id\""})
		return
	}
	severity := leakSeverity(req.Severity, req.H2Ppm)

	// Per-sensor dedup: fold into the still-active incident for this sensor
	// (and refresh its worst observed ppm/severity) instead of opening a new
	// one per reading.
	// Severity may only escalate on a repeat reading, never de-escalate.
	existing, err := scanIncident(h.db.QueryRow(r.Context(), `
		UPDATE infra.incidents
		SET severity = CASE
				WHEN array_position(ARRAY['low','medium','high','critical'], severity) <
				     array_position(ARRAY['low','medium','high','critical'], $2)
				THEN $2 ELSE severity END,
			meta = meta || jsonb_build_object(
				'h2_ppm', COALESCE(to_jsonb($3::float8), meta->'h2_ppm'),
				'readings', COALESCE((meta->>'readings')::int, 1) + 1,
				'last_reading_at', to_jsonb(now()))
		WHERE type = 'h2_leak'
		  AND meta->>'sensor_id' = $1
		  AND status IN ('open','acknowledged','in_progress')
		  AND opened_at > now() - interval '`+leakDedupWindow+`'
		RETURNING `+incidentCols,
		req.SensorID, severity, req.H2Ppm))
	switch {
	case err == nil:
		// Folded into an active incident; no new row, no new workflow.
		writeJSON(w, http.StatusOK, map[string]any{"incident": existing, "deduplicated": true})
		return
	case !errors.Is(err, pgx.ErrNoRows):
		h.internal(w, "dedup leak event", err)
		return
	}

	incident, err := h.insertIncident(r, createIncidentRequest{
		Type:      "h2_leak",
		Severity:  severity,
		BusID:     req.BusID,
		StationID: req.StationID,
		Meta: map[string]any{
			"sensor_id": req.SensorID,
			"h2_ppm":    req.H2Ppm,
			"location":  req.Location,
			"readings":  1,
		},
	})
	if err != nil {
		h.internal(w, "ingest leak event", err)
		return
	}

	event := map[string]any{
		"incident_id": incident.ID,
		"sensor_id":   req.SensorID,
		"severity":    incident.Severity,
		"bus_id":      req.BusID,
		"station_id":  req.StationID,
		"h2_ppm":      req.H2Ppm,
		"location":    req.Location,
	}
	if err := h.pub.Publish(r.Context(), "safety.leak.detected", event); err != nil {
		h.log.Error("failed to publish safety.leak.detected", zap.Error(err))
	}
	if err := h.wf.Signal(r.Context(), "incident-"+incident.ID, "leak-detected", event); err != nil {
		h.log.Error("failed to signal incident workflow", zap.String("incident", incident.ID), zap.Error(err))
	}
	h.afterIncidentOpened(r, incident)

	writeJSON(w, http.StatusAccepted, map[string]any{"incident": incident})
}

// TriggerSOS handles POST /v1/safety/sos (Keycloak JWT, driver role) —
// Wave-6 A4-01: the driver's panic channel. Opens a CRITICAL incident of
// type 'sos' enriched with the driver's active dispatch job (vehicle,
// route) and the vehicle's latest telemetry position, publishes safety.sos,
// starts the standard incident-response workflow (shared ingress signal:
// in_progress → escalation timer → ack/resolve), and fires partner webhooks
// via afterIncidentOpened.
func (h *Handler) TriggerSOS(w http.ResponseWriter, r *http.Request) {
	subject := auth.Subject(r.Context())
	if subject == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "authenticated subject required"})
		return
	}
	var req struct {
		Description string `json:"description"`
	}
	if r.Body != nil && r.ContentLength != 0 {
		if err := decodeJSON(w, r, &req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
			return
		}
	}

	meta := map[string]any{"driver_sub": subject, "source": "driver_sos"}
	if req.Description != "" {
		meta["description"] = req.Description
	}
	// Attach the driver's active dispatch job (best-effort context).
	var vehicleID, route *string
	if err := h.db.QueryRow(r.Context(), `
		SELECT vehicle_id, NULLIF(route,'') FROM infra.dispatch_jobs
		WHERE driver_sub = $1 AND status IN ('accepted','in_progress')
		ORDER BY COALESCE(starts_at, created_at) DESC LIMIT 1`, subject).
		Scan(&vehicleID, &route); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		h.internal(w, "load driver dispatch context", err)
		return
	}
	if vehicleID != nil {
		meta["vehicle_id"] = *vehicleID
	}
	if route != nil {
		meta["route"] = *route
	}
	// Attach the vehicle's last known position (best-effort).
	if vehicleID != nil {
		var lat, lon float64
		if err := h.db.QueryRow(r.Context(), `
			SELECT ST_Y(geom)::float8, ST_X(geom)::float8 FROM fleet.telemetry
			WHERE bus_id = $1 ORDER BY ts DESC LIMIT 1`, *vehicleID).Scan(&lat, &lon); err == nil {
			meta["lat"] = lat
			meta["lon"] = lon
		} else if !errors.Is(err, pgx.ErrNoRows) {
			h.internal(w, "load vehicle position", err)
			return
		}
	}

	incident, err := h.insertIncident(r, createIncidentRequest{
		Type:      "sos",
		Severity:  "critical",
		BusID:     vehicleID,
		StationID: nil,
		Meta:      meta,
	})
	if err != nil {
		h.internal(w, "open sos incident", err)
		return
	}

	event := map[string]any{
		"incident_id": incident.ID,
		"driver_sub":  subject,
		"severity":    incident.Severity,
		"vehicle_id":  vehicleID,
		"route":       route,
	}
	if err := h.pub.Publish(r.Context(), "safety.sos", event); err != nil {
		h.log.Error("failed to publish safety.sos", zap.Error(err))
	}
	// The incident-response workflow's ingress signal is shared by every
	// critical incident (SignalWithStart starts the instance when absent).
	if err := h.wf.Signal(r.Context(), "incident-"+incident.ID, "leak-detected", event); err != nil {
		h.log.Error("failed to signal incident workflow", zap.String("incident", incident.ID), zap.Error(err))
	}
	h.afterIncidentOpened(r, incident)

	writeJSON(w, http.StatusCreated, map[string]any{"incident": incident})
}
