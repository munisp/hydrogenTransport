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

// DispatchJob mirrors infra.dispatch_jobs (dispatch-workforce module).
type DispatchJob struct {
	ID         string     `json:"id"`
	DriverSub  string     `json:"driver_sub"`
	VehicleID  *string    `json:"vehicle_id,omitempty"`
	Route      string     `json:"route"`
	StartsAt   *time.Time `json:"starts_at,omitempty"`
	Status     string     `json:"status"`
	CreatedAt  time.Time  `json:"created_at"`
	AcceptedAt *time.Time `json:"accepted_at,omitempty"`
}

const dispatchJobCols = `id, driver_sub, vehicle_id, route, starts_at, status, created_at, accepted_at`

// timeValue formats a nullable timestamp as RFC3339 for event payloads
// (nil → null).
func timeValue(t *time.Time) any {
	if t == nil {
		return nil
	}
	return t.UTC().Format(time.RFC3339)
}

// ListDispatchJobs handles GET /v1/dispatch/jobs?status=.
func (h *Handler) ListDispatchJobs(w http.ResponseWriter, r *http.Request) {
	query := `SELECT ` + dispatchJobCols + ` FROM infra.dispatch_jobs`
	args := []any{}
	if status := r.URL.Query().Get("status"); status != "" {
		query += ` WHERE status = $1`
		args = append(args, status)
	}
	query += ` ORDER BY created_at DESC LIMIT 200`

	rows, err := h.db.Query(r.Context(), query, args...)
	if err != nil {
		h.internal(w, "list dispatch jobs", err)
		return
	}
	defer rows.Close()

	jobs := []DispatchJob{}
	for rows.Next() {
		var j DispatchJob
		if err := rows.Scan(&j.ID, &j.DriverSub, &j.VehicleID, &j.Route, &j.StartsAt, &j.Status, &j.CreatedAt, &j.AcceptedAt); err != nil {
			h.internal(w, "scan dispatch job", err)
			return
		}
		jobs = append(jobs, j)
	}
	if err := rows.Err(); err != nil {
		h.internal(w, "iterate dispatch jobs", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"jobs": jobs})
}

type createDispatchJobRequest struct {
	DriverSub string     `json:"driver_sub"`
	VehicleID *string    `json:"vehicle_id"`
	Route     string     `json:"route"`
	StartsAt  *time.Time `json:"starts_at"`
}

// CreateDispatchJob handles POST /v1/dispatch/jobs (Keycloak JWT).
// Publishes dispatch.job.assigned (SPEC §3.3) and signals the Temporal
// dispatch workflow.
func (h *Handler) CreateDispatchJob(w http.ResponseWriter, r *http.Request) {
	var req createDispatchJobRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.DriverSub == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "body must include \"driver_sub\""})
		return
	}
	var j DispatchJob
	err := h.db.QueryRow(r.Context(), `
		INSERT INTO infra.dispatch_jobs (driver_sub, vehicle_id, route, starts_at)
		VALUES ($1, $2, $3, $4)
		RETURNING `+dispatchJobCols, req.DriverSub, req.VehicleID, req.Route, req.StartsAt).
		Scan(&j.ID, &j.DriverSub, &j.VehicleID, &j.Route, &j.StartsAt, &j.Status, &j.CreatedAt, &j.AcceptedAt)
	if err != nil {
		h.internal(w, "create dispatch job", err)
		return
	}

	// dispatch.job.assigned payload per
	// packages/events/schemas/dispatch.job.assigned.json: driver_id (from the
	// driver subject), bus_id (from vehicle_id), route_id (from route),
	// shift_start (from starts_at). shift_end has no source column yet and is
	// published as null (schema: optional).
	event := map[string]any{
		"job_id":      j.ID,
		"driver_id":   j.DriverSub,
		"bus_id":      j.VehicleID,
		"route_id":    j.Route,
		"shift_start": timeValue(j.StartsAt),
		"shift_end":   nil,
	}
	if err := h.pub.Publish(r.Context(), "dispatch.job.assigned", event); err != nil {
		h.log.Error("failed to publish dispatch.job.assigned", zap.Error(err))
	}
	if err := h.wf.Signal(r.Context(), "dispatch-"+j.ID, "job-assigned", event); err != nil {
		h.log.Error("failed to signal dispatch workflow", zap.String("job", j.ID), zap.Error(err))
	}

	writeJSON(w, http.StatusCreated, j)
}

// AcceptDispatchJob handles POST /v1/dispatch/jobs/{id}/accept (Keycloak JWT):
// marks an assigned job accepted and stamps accepted_at.
func (h *Handler) AcceptDispatchJob(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var j DispatchJob
	err := h.db.QueryRow(r.Context(), `
		UPDATE infra.dispatch_jobs
		SET status = 'accepted', accepted_at = now()
		WHERE id = $1 AND status = 'assigned'
		RETURNING `+dispatchJobCols, id).
		Scan(&j.ID, &j.DriverSub, &j.VehicleID, &j.Route, &j.StartsAt, &j.Status, &j.CreatedAt, &j.AcceptedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "job not found or not in assigned status"})
		return
	}
	if err != nil {
		h.internal(w, "accept dispatch job", err)
		return
	}
	// Signal the dispatch workflow so the accept timeout is cancelled.
	if err := h.wf.Signal(r.Context(), "dispatch-"+j.ID, "job-accepted",
		map[string]any{"job_id": j.ID, "driver_id": j.DriverSub}); err != nil {
		h.log.Error("failed to signal dispatch accept", zap.String("job", j.ID), zap.Error(err))
	}
	writeJSON(w, http.StatusOK, j)
}
