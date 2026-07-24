package handlers

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"go.uber.org/zap"
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
	Type      string         `json:"type"`
	Severity  string         `json:"severity"`
	BusID     *string        `json:"bus_id"`
	StationID *string        `json:"station_id"`
	Meta      map[string]any `json:"meta"`
}

// OpenIncident handles POST /v1/incidents (Keycloak JWT).
func (h *Handler) OpenIncident(w http.ResponseWriter, r *http.Request) {
	var req createIncidentRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Type == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "body must include \"type\""})
		return
	}
	if req.Severity == "" {
		req.Severity = "medium"
	}
	i, err := h.insertIncident(r, req)
	if err != nil {
		h.internal(w, "open incident", err)
		return
	}
	writeJSON(w, http.StatusCreated, i)
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

// transitionIncident moves an incident to a new status (ack/resolve).
func (h *Handler) transitionIncident(w http.ResponseWriter, r *http.Request, from, to string) {
	id := chi.URLParam(r, "id")
	i, err := scanIncident(h.db.QueryRow(r.Context(), `
		UPDATE infra.incidents SET status = $2
		WHERE id = $1 AND status = $3
		RETURNING `+incidentCols, id, to, from))
	if errors.Is(err, pgx.ErrNoRows) {
		writeJSON(w, http.StatusConflict, map[string]string{
			"error": "incident not found or not in status '" + from + "'",
		})
		return
	}
	if err != nil {
		h.internal(w, "transition incident", err)
		return
	}
	writeJSON(w, http.StatusOK, i)
}

// AckIncident handles POST /v1/incidents/{id}/ack (Keycloak JWT).
func (h *Handler) AckIncident(w http.ResponseWriter, r *http.Request) {
	h.transitionIncident(w, r, "open", "acknowledged")
}

// ResolveIncident handles POST /v1/incidents/{id}/resolve (Keycloak JWT).
func (h *Handler) ResolveIncident(w http.ResponseWriter, r *http.Request) {
	// Allow resolving both open and acknowledged incidents.
	id := chi.URLParam(r, "id")
	i, err := scanIncident(h.db.QueryRow(r.Context(), `
		UPDATE infra.incidents SET status = 'resolved'
		WHERE id = $1 AND status IN ('open','acknowledged')
		RETURNING `+incidentCols, id))
	if errors.Is(err, pgx.ErrNoRows) {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "incident not found or already resolved"})
		return
	}
	if err != nil {
		h.internal(w, "resolve incident", err)
		return
	}
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

// IngestLeak handles POST /v1/safety/leak — the H2 sensor ingestion webhook
// (leak-detection module; authenticated by sensor token or JWT at the router).
// It opens an incident, publishes safety.leak.detected (SPEC §3.3) and signals
// the Temporal incident-response workflow.
func (h *Handler) IngestLeak(w http.ResponseWriter, r *http.Request) {
	var req leakEventRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.SensorID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "body must include \"sensor_id\""})
		return
	}
	if req.Severity == "" {
		req.Severity = "high"
	}

	incident, err := h.insertIncident(r, createIncidentRequest{
		Type:      "h2_leak",
		Severity:  req.Severity,
		BusID:     req.BusID,
		StationID: req.StationID,
		Meta: map[string]any{
			"sensor_id": req.SensorID,
			"h2_ppm":    req.H2Ppm,
			"location":  req.Location,
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

	writeJSON(w, http.StatusAccepted, map[string]any{"incident": incident})
}
