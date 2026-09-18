package handlers

import (
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"go.uber.org/zap"

	auth "github.com/munisp/hydrogenTransport/packages/go-auth"
)

// Wave-7 A2-09: charter/school block bookings.
//
// A charter booking reserves N vehicles for a fixed window (a school trip, a
// corporate shuttle, an event). Instead of a bespoke locking mechanism the
// booking is backed by the machinery dispatch already enforces: per reserved
// vehicle the booking transaction inserts
//
//   - a placeholder driver row (sub = "charter:<reference>:<vehicle_id>",
//     status 'off-duty') so the NOT VALID driver FK on dispatch_jobs holds,
//   - one dispatch job (route = 'charter:<reference>') for the window.
//
// The existing overlap engine then does the rest with zero special-casing:
// CreateDispatchJob rejects an overlapping regular job on a charter vehicle
// (409), a second charter on the same vehicle conflicts the same way, the
// partial unique vehicle/driver indexes (0005) make concurrent races safe,
// and the DRT dispatch-busy guard sees the vehicle as booked. Cancelling the
// booking cancels its dispatch jobs, freeing the vehicles.

// CharterBooking mirrors infra.charter_bookings (0010).
type CharterBooking struct {
	ID           string           `json:"id"`
	Reference    string           `json:"reference"`
	CustomerName string           `json:"customer_name"`
	Contact      string           `json:"contact"`
	StartsAt     time.Time        `json:"starts_at"`
	EndsAt       time.Time        `json:"ends_at"`
	Status       string           `json:"status"`
	Notes        string           `json:"notes"`
	CreatedBy    string           `json:"created_by"`
	CreatedAt    time.Time        `json:"created_at"`
	CancelledAt  *time.Time       `json:"cancelled_at,omitempty"`
	VehicleCount int              `json:"vehicle_count,omitempty"`
	Vehicles     []CharterVehicle `json:"vehicles,omitempty"`
}

// CharterVehicle is one reserved vehicle and its backing dispatch job.
type CharterVehicle struct {
	VehicleID     string `json:"vehicle_id"`
	DispatchJobID string `json:"dispatch_job_id"`
}

const charterBookingCols = `id, reference, customer_name, contact, starts_at, ends_at, status, notes, created_by, created_at, cancelled_at`

func scanCharterBooking(row pgx.Row) (CharterBooking, error) {
	var b CharterBooking
	err := row.Scan(&b.ID, &b.Reference, &b.CustomerName, &b.Contact, &b.StartsAt, &b.EndsAt,
		&b.Status, &b.Notes, &b.CreatedBy, &b.CreatedAt, &b.CancelledAt)
	return b, err
}

// charterDriverSub is the placeholder driver subject backing the dispatch
// job of one chartered vehicle (unique per vehicle so the partial unique
// driver index never collides inside one booking).
func charterDriverSub(reference, vehicleID string) string {
	return "charter:" + reference + ":" + vehicleID
}

type createCharterRequest struct {
	Reference    string    `json:"reference"`
	CustomerName string    `json:"customer_name"`
	Contact      string    `json:"contact"`
	StartsAt     time.Time `json:"starts_at"`
	EndsAt       time.Time `json:"ends_at"`
	VehicleIDs   []string  `json:"vehicle_ids"`
	Notes        string    `json:"notes"`
}

// CreateCharter handles POST /v1/charters (Keycloak JWT, operator). One
// transaction: insert the booking header (idempotent on the customer-facing
// reference — a retried create replays the existing booking with 200), then
// per vehicle the dispatch overlap check + placeholder driver + charter
// dispatch job + charter_vehicles link. Publishes charter.booking.confirmed.
func (h *Handler) CreateCharter(w http.ResponseWriter, r *http.Request) {
	var req createCharterRequest
	if err := decodeJSON(w, r, &req); err != nil || req.Reference == "" || req.CustomerName == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "body must include \"reference\" and \"customer_name\""})
		return
	}
	if req.StartsAt.IsZero() || req.EndsAt.IsZero() || !req.EndsAt.After(req.StartsAt) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "starts_at and ends_at are required and ends_at must be after starts_at"})
		return
	}
	if len(req.VehicleIDs) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "vehicle_ids must name at least one vehicle"})
		return
	}
	// Duplicate vehicles in one booking would collide on the
	// charter_vehicles primary key — reject them up front with a clear 400.
	seen := map[string]bool{}
	for _, v := range req.VehicleIDs {
		if v == "" || seen[v] {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "vehicle_ids must be distinct, non-empty vehicle ids"})
			return
		}
		seen[v] = true
	}
	operator := auth.Subject(r.Context()) // "" only if middleware was bypassed

	tx, err := h.db.Begin(r.Context())
	if err != nil {
		h.internal(w, "begin charter booking", err)
		return
	}
	defer func() { _ = tx.Rollback(r.Context()) }()

	// Serialize concurrent creates of the same reference so the idempotent
	// replay below observes a committed booking, not an in-flight one.
	if _, err := tx.Exec(r.Context(), `SELECT pg_advisory_xact_lock(hashtext($1))`, "charter:"+req.Reference); err != nil {
		h.internal(w, "lock charter reference", err)
		return
	}

	b, err := scanCharterBooking(tx.QueryRow(r.Context(), `
		INSERT INTO infra.charter_bookings (reference, customer_name, contact, starts_at, ends_at, notes, created_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		RETURNING `+charterBookingCols,
		req.Reference, req.CustomerName, req.Contact, req.StartsAt, req.EndsAt, req.Notes, operator))
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" { // reference taken
			existing, qerr := scanCharterBooking(h.db.QueryRow(r.Context(), `
				SELECT `+charterBookingCols+` FROM infra.charter_bookings WHERE reference = $1`, req.Reference))
			if qerr != nil {
				h.internal(w, "load existing charter booking", qerr)
				return
			}
			// Same reference, same window + vehicles = the client's retry:
			// replay the booking. A DIFFERENT booking reusing the reference
			// is a real conflict.
			if existing.CustomerName == req.CustomerName &&
				existing.StartsAt.Equal(req.StartsAt) && existing.EndsAt.Equal(req.EndsAt) {
				writeJSON(w, http.StatusOK, existing)
				return
			}
			writeJSON(w, http.StatusConflict, map[string]string{"error": "reference already used by a different charter booking"})
			return
		}
		h.internal(w, "insert charter booking", err)
		return
	}

	vehicles := make([]CharterVehicle, 0, len(req.VehicleIDs))
	for _, vehicleID := range req.VehicleIDs {
		// Same overlap semantics as CreateDispatchJob (both bounds are
		// always set for a charter): any active job on the vehicle whose
		// window intersects [starts_at, ends_at) blocks the booking.
		var conflict bool
		if err := tx.QueryRow(r.Context(), `
			SELECT EXISTS (
				SELECT 1 FROM infra.dispatch_jobs
				WHERE vehicle_id = $1
				  AND status IN ('assigned','accepted','in_progress')
				  AND (starts_at IS NULL OR starts_at < $3)
				  AND (ends_at IS NULL OR ends_at > $2)
			)`, vehicleID, req.StartsAt, req.EndsAt).Scan(&conflict); err != nil {
			h.internal(w, "charter vehicle conflict check", err)
			return
		}
		if conflict {
			writeJSON(w, http.StatusConflict, map[string]string{
				"error": "vehicle " + vehicleID + " already has an overlapping active dispatch job"})
			return
		}

		driverSub := charterDriverSub(req.Reference, vehicleID)
		if _, err := tx.Exec(r.Context(), `
			INSERT INTO infra.drivers (sub, name, status)
			VALUES ($1, $2, 'off-duty')
			ON CONFLICT (sub) DO NOTHING`,
			driverSub, "Charter placeholder "+req.Reference); err != nil {
			h.internal(w, "insert charter placeholder driver", err)
			return
		}

		var jobID string
		err := tx.QueryRow(r.Context(), `
			INSERT INTO infra.dispatch_jobs (driver_sub, vehicle_id, route, starts_at, ends_at)
			VALUES ($1, $2, $3, $4, $5)
			RETURNING id`, driverSub, vehicleID, "charter:"+req.Reference, req.StartsAt, req.EndsAt).Scan(&jobID)
		if err != nil {
			var pgErr *pgconn.PgError
			if errors.As(err, &pgErr) && pgErr.Code == "23503" { // FK: unknown vehicle
				writeJSON(w, http.StatusUnprocessableEntity, map[string]string{
					"error": "vehicle " + vehicleID + " does not reference a known vehicle"})
				return
			}
			if errors.As(err, &pgErr) && pgErr.Code == "23505" { // raced double-booking
				writeJSON(w, http.StatusConflict, map[string]string{
					"error": "vehicle " + vehicleID + " was booked concurrently"})
				return
			}
			h.internal(w, "insert charter dispatch job", err)
			return
		}

		if _, err := tx.Exec(r.Context(), `
			INSERT INTO infra.charter_vehicles (booking_id, vehicle_id, dispatch_job_id)
			VALUES ($1, $2, $3)`, b.ID, vehicleID, jobID); err != nil {
			h.internal(w, "link charter vehicle", err)
			return
		}
		vehicles = append(vehicles, CharterVehicle{VehicleID: vehicleID, DispatchJobID: jobID})
	}

	if err := tx.Commit(r.Context()); err != nil {
		h.internal(w, "commit charter booking", err)
		return
	}

	b.Vehicles = vehicles
	if err := h.pub.Publish(r.Context(), "charter.booking.confirmed", map[string]any{
		"booking_id":  b.ID,
		"reference":   b.Reference,
		"customer":    b.CustomerName,
		"vehicle_ids": req.VehicleIDs,
		"starts_at":   b.StartsAt.UTC().Format(time.RFC3339),
		"ends_at":     b.EndsAt.UTC().Format(time.RFC3339),
	}); err != nil {
		h.log.Error("failed to publish charter.booking.confirmed", zap.Error(err))
	}

	writeJSON(w, http.StatusCreated, b)
}

// ListCharters handles GET /v1/charters?status= (Keycloak JWT, operator).
func (h *Handler) ListCharters(w http.ResponseWriter, r *http.Request) {
	query := `SELECT ` + charterBookingCols + `,
		(SELECT count(*) FROM infra.charter_vehicles cv WHERE cv.booking_id = b.id) AS vehicle_count
		FROM infra.charter_bookings b`
	args := []any{}
	if status := r.URL.Query().Get("status"); status != "" {
		args = append(args, status)
		query += ` WHERE b.status = $1`
	}
	query += ` ORDER BY b.starts_at DESC LIMIT 200`

	rows, err := h.db.Query(r.Context(), query, args...)
	if err != nil {
		h.internal(w, "list charter bookings", err)
		return
	}
	defer rows.Close()

	bookings := []CharterBooking{}
	for rows.Next() {
		var b CharterBooking
		if err := rows.Scan(&b.ID, &b.Reference, &b.CustomerName, &b.Contact, &b.StartsAt, &b.EndsAt,
			&b.Status, &b.Notes, &b.CreatedBy, &b.CreatedAt, &b.CancelledAt, &b.VehicleCount); err != nil {
			h.internal(w, "scan charter booking", err)
			return
		}
		bookings = append(bookings, b)
	}
	if err := rows.Err(); err != nil {
		h.internal(w, "iterate charter bookings", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"bookings": bookings})
}

// GetCharter handles GET /v1/charters/{id} (Keycloak JWT, operator):
// booking header plus every reserved vehicle and its backing dispatch job.
func (h *Handler) GetCharter(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	b, err := scanCharterBooking(h.db.QueryRow(r.Context(), `
		SELECT `+charterBookingCols+` FROM infra.charter_bookings WHERE id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "charter booking not found"})
		return
	}
	if err != nil {
		h.internal(w, "load charter booking", err)
		return
	}

	rows, err := h.db.Query(r.Context(), `
		SELECT vehicle_id, dispatch_job_id FROM infra.charter_vehicles
		WHERE booking_id = $1 ORDER BY vehicle_id`, id)
	if err != nil {
		h.internal(w, "list charter vehicles", err)
		return
	}
	defer rows.Close()
	b.Vehicles = []CharterVehicle{}
	for rows.Next() {
		var v CharterVehicle
		if err := rows.Scan(&v.VehicleID, &v.DispatchJobID); err != nil {
			h.internal(w, "scan charter vehicle", err)
			return
		}
		b.Vehicles = append(b.Vehicles, v)
	}
	if err := rows.Err(); err != nil {
		h.internal(w, "iterate charter vehicles", err)
		return
	}
	writeJSON(w, http.StatusOK, b)
}

// CancelCharter handles POST /v1/charters/{id}/cancel (Keycloak JWT,
// operator): cancels the booking AND every active backing dispatch job in
// one transaction, freeing the vehicles for regular dispatch immediately.
// Each cancelled job also gets the dispatch workflow's job-cancelled signal.
func (h *Handler) CancelCharter(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	tx, err := h.db.Begin(r.Context())
	if err != nil {
		h.internal(w, "begin charter cancellation", err)
		return
	}
	defer func() { _ = tx.Rollback(r.Context()) }()

	b, err := scanCharterBooking(tx.QueryRow(r.Context(), `
		UPDATE infra.charter_bookings
		SET status = 'cancelled', cancelled_at = now()
		WHERE id = $1 AND status = 'confirmed'
		RETURNING `+charterBookingCols, id))
	if errors.Is(err, pgx.ErrNoRows) {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "charter booking not found or not in confirmed status"})
		return
	}
	if err != nil {
		h.internal(w, "cancel charter booking", err)
		return
	}

	jobRows, err := tx.Query(r.Context(), `
		UPDATE infra.dispatch_jobs SET status = 'cancelled'
		WHERE id IN (SELECT dispatch_job_id FROM infra.charter_vehicles WHERE booking_id = $1)
		  AND status IN ('assigned','accepted','in_progress')
		RETURNING id`, id)
	if err != nil {
		h.internal(w, "cancel charter dispatch jobs", err)
		return
	}
	cancelledJobs := []string{}
	for jobRows.Next() {
		var jobID string
		if err := jobRows.Scan(&jobID); err != nil {
			jobRows.Close()
			h.internal(w, "scan cancelled charter job", err)
			return
		}
		cancelledJobs = append(cancelledJobs, jobID)
	}
	jobRows.Close()
	if err := jobRows.Err(); err != nil {
		h.internal(w, "iterate cancelled charter jobs", err)
		return
	}

	if err := tx.Commit(r.Context()); err != nil {
		h.internal(w, "commit charter cancellation", err)
		return
	}

	for _, jobID := range cancelledJobs {
		if err := h.wf.Signal(r.Context(), "dispatch-"+jobID, "job-cancelled",
			map[string]any{"job_id": jobID, "charter_reference": b.Reference}); err != nil {
			h.log.Error("failed to signal charter job cancel", zap.String("job", jobID), zap.Error(err))
		}
	}
	if err := h.pub.Publish(r.Context(), "charter.booking.cancelled", map[string]any{
		"booking_id": b.ID,
		"reference":  b.Reference,
		"jobs":       cancelledJobs,
	}); err != nil {
		h.log.Error("failed to publish charter.booking.cancelled", zap.Error(err))
	}

	writeJSON(w, http.StatusOK, b)
}
