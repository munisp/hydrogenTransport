package handlers

import (
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"go.uber.org/zap"
)

// DispatchJob mirrors infra.dispatch_jobs (dispatch-workforce module).
type DispatchJob struct {
	ID         string     `json:"id"`
	DriverSub  string     `json:"driver_sub"`
	VehicleID  *string    `json:"vehicle_id,omitempty"`
	Route      string     `json:"route"`
	StartsAt   *time.Time `json:"starts_at,omitempty"`
	EndsAt     *time.Time `json:"ends_at,omitempty"`
	Status     string     `json:"status"`
	CreatedAt  time.Time  `json:"created_at"`
	AcceptedAt *time.Time `json:"accepted_at,omitempty"`
}

const dispatchJobCols = `id, driver_sub, vehicle_id, route, starts_at, ends_at, status, created_at, accepted_at`

func scanDispatchJob(row pgx.Row) (DispatchJob, error) {
	var j DispatchJob
	err := row.Scan(&j.ID, &j.DriverSub, &j.VehicleID, &j.Route, &j.StartsAt, &j.EndsAt, &j.Status, &j.CreatedAt, &j.AcceptedAt)
	return j, err
}

// timeValue formats a nullable timestamp as RFC3339 for event payloads
// (nil → null).
func timeValue(t *time.Time) any {
	if t == nil {
		return nil
	}
	return t.UTC().Format(time.RFC3339)
}

// ListDispatchJobs handles GET /v1/dispatch/jobs?status=&driver_sub=.
// driver_sub filters to one driver's jobs (the mobile DriverScreen sends it;
// it was previously accepted and silently ignored — BUSINESS_LOGIC_AUDIT §8).
func (h *Handler) ListDispatchJobs(w http.ResponseWriter, r *http.Request) {
	query := `SELECT ` + dispatchJobCols + ` FROM infra.dispatch_jobs`
	args := []any{}
	conds := []string{}
	if status := r.URL.Query().Get("status"); status != "" {
		args = append(args, status)
		conds = append(conds, `status = $1`)
	}
	if driver := r.URL.Query().Get("driver_sub"); driver != "" {
		args = append(args, driver)
		conds = append(conds, `driver_sub = $2`)
	}
	if len(conds) > 0 {
		query += ` WHERE ` + conds[0]
		if len(conds) > 1 {
			query += ` AND ` + conds[1]
		}
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
		if err := rows.Scan(&j.ID, &j.DriverSub, &j.VehicleID, &j.Route, &j.StartsAt, &j.EndsAt, &j.Status, &j.CreatedAt, &j.AcceptedAt); err != nil {
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
	EndsAt    *time.Time `json:"ends_at"`
}

// CreateDispatchJob handles POST /v1/dispatch/jobs (Keycloak JWT).
// Publishes dispatch.job.assigned (SPEC §3.3) and signals the Temporal
// dispatch workflow.
//
// Conflict rules (BUSINESS_LOGIC_AUDIT §8): a driver must not hold two
// overlapping active jobs and a vehicle must not be double-booked. Active =
// assigned|accepted|in_progress. When both starts_at and ends_at are given
// the overlap test is a real time-window intersection; with open-ended
// windows (either bound null) any shared active job conflicts. The check
// runs inside the insert transaction and is backed by partial unique indexes
// (migration 0005) so a concurrent create cannot race past it.
func (h *Handler) CreateDispatchJob(w http.ResponseWriter, r *http.Request) {
	var req createDispatchJobRequest
	if err := decodeJSON(w, r, &req); err != nil || req.DriverSub == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "body must include \"driver_sub\""})
		return
	}
	if req.StartsAt != nil && req.EndsAt != nil && !req.EndsAt.After(*req.StartsAt) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "ends_at must be after starts_at"})
		return
	}

	tx, err := h.db.Begin(r.Context())
	if err != nil {
		h.internal(w, "begin dispatch job transaction", err)
		return
	}
	defer func() { _ = tx.Rollback(r.Context()) }()

	// Time-window overlap: [starts_at, ends_at) vs the candidate window; a
	// null bound is open-ended (overlaps everything on its side).
	var conflict string
	err = tx.QueryRow(r.Context(), `
		SELECT CASE WHEN driver_sub = $1 THEN 'driver' ELSE 'vehicle' END
		FROM infra.dispatch_jobs
		WHERE status IN ('assigned','accepted','in_progress')
		  AND (driver_sub = $1 OR ($2::uuid IS NOT NULL AND vehicle_id = $2))
		  AND ($3::timestamptz IS NULL OR starts_at IS NULL OR starts_at < $4::timestamptz)
		  AND ($4::timestamptz IS NULL OR ends_at IS NULL OR ends_at > $3::timestamptz)
		LIMIT 1`, req.DriverSub, req.VehicleID, req.StartsAt, req.EndsAt).Scan(&conflict)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		h.internal(w, "dispatch conflict check", err)
		return
	}
	if err == nil {
		if conflict == "driver" {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "driver already has an overlapping active dispatch job"})
		} else {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "vehicle already assigned to an overlapping active dispatch job"})
		}
		return
	}

	var j DispatchJob
	err = tx.QueryRow(r.Context(), `
		INSERT INTO infra.dispatch_jobs (driver_sub, vehicle_id, route, starts_at, ends_at)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING `+dispatchJobCols, req.DriverSub, req.VehicleID, req.Route, req.StartsAt, req.EndsAt).
		Scan(&j.ID, &j.DriverSub, &j.VehicleID, &j.Route, &j.StartsAt, &j.EndsAt, &j.Status, &j.CreatedAt, &j.AcceptedAt)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23503" { // FK: unknown vehicle/driver (0005 NOT VALID FKs)
			writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": "vehicle_id or driver_sub does not reference a known vehicle/driver"})
			return
		}
		if errors.As(err, &pgErr) && pgErr.Code == "23505" { // partial unique index: raced double-booking
			writeJSON(w, http.StatusConflict, map[string]string{"error": "driver or vehicle already has an active dispatch job"})
			return
		}
		h.internal(w, "create dispatch job", err)
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		h.internal(w, "commit dispatch job", err)
		return
	}

	// dispatch.job.assigned payload per
	// packages/events/schemas/dispatch.job.assigned.json: driver_id (from the
	// driver subject), bus_id (from vehicle_id), route_id (from route),
	// shift_start/shift_end (from starts_at/ends_at; null when open-ended).
	event := map[string]any{
		"job_id":      j.ID,
		"driver_id":   j.DriverSub,
		"bus_id":      j.VehicleID,
		"route_id":    j.Route,
		"shift_start": timeValue(j.StartsAt),
		"shift_end":   timeValue(j.EndsAt),
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
	j, err := scanDispatchJob(h.db.QueryRow(r.Context(), `
		UPDATE infra.dispatch_jobs
		SET status = 'accepted', accepted_at = now()
		WHERE id = $1 AND status = 'assigned'
		RETURNING `+dispatchJobCols, id))
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

// CancelDispatchJob handles POST /v1/dispatch/jobs/{id}/cancel (Keycloak JWT,
// operator): cancels an active job and delivers the job-cancelled signal the
// dispatch workflow already handles (BUSINESS_LOGIC_AUDIT §8: the signal
// path existed but no endpoint could deliver it).
func (h *Handler) CancelDispatchJob(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	j, err := scanDispatchJob(h.db.QueryRow(r.Context(), `
		UPDATE infra.dispatch_jobs
		SET status = 'cancelled'
		WHERE id = $1 AND status IN ('assigned','accepted','in_progress')
		RETURNING `+dispatchJobCols, id))
	if errors.Is(err, pgx.ErrNoRows) {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "job not found or not in an active status"})
		return
	}
	if err != nil {
		h.internal(w, "cancel dispatch job", err)
		return
	}
	if err := h.wf.Signal(r.Context(), "dispatch-"+j.ID, "job-cancelled",
		map[string]any{"job_id": j.ID, "driver_id": j.DriverSub}); err != nil {
		h.log.Error("failed to signal dispatch cancel", zap.String("job", j.ID), zap.Error(err))
	}
	writeJSON(w, http.StatusOK, j)
}
