package handlers

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"go.uber.org/zap"

	auth "github.com/munisp/hydrogenTransport/packages/go-auth"
)

// DRTRequest mirrors citizen.drt_requests (demand-responsive module).
// PickupLabel/DropoffLabel/Passengers come from migration 0005 S3 (the PWA
// already sent them; they were silently dropped — BUSINESS_LOGIC_AUDIT
// §13). VehicleID/DriverSub/AssignedAt (0005 S4) make the assigned status
// reachable.
type DRTRequest struct {
	ID           string     `json:"id"`
	UserSub      string     `json:"user_sub"`
	PickupLat    *float64   `json:"pickup_lat,omitempty"`
	PickupLon    *float64   `json:"pickup_lon,omitempty"`
	DropoffLat   *float64   `json:"dropoff_lat,omitempty"`
	DropoffLon   *float64   `json:"dropoff_lon,omitempty"`
	PickupLabel  *string    `json:"pickup_label,omitempty"`
	DropoffLabel *string    `json:"dropoff_label,omitempty"`
	Passengers   *int       `json:"passengers,omitempty"`
	VehicleID    *string    `json:"vehicle_id,omitempty"`
	DriverSub    *string    `json:"driver_sub,omitempty"`
	AssignedAt   *time.Time `json:"assigned_at,omitempty"`
	Status       string     `json:"status"`
	RequestedAt  time.Time  `json:"requested_at"`
}

const drtCols = `id, user_sub,
	ST_Y(pickup)::float8, ST_X(pickup)::float8,
	ST_Y(dropoff)::float8, ST_X(dropoff)::float8,
	NULLIF(pickup_label,''), NULLIF(dropoff_label,''), passengers,
	vehicle_id, driver_sub, assigned_at,
	status, requested_at`

func scanDRT(row pgx.Row) (DRTRequest, error) {
	var d DRTRequest
	err := row.Scan(&d.ID, &d.UserSub, &d.PickupLat, &d.PickupLon, &d.DropoffLat, &d.DropoffLon,
		&d.PickupLabel, &d.DropoffLabel, &d.Passengers, &d.VehicleID, &d.DriverSub, &d.AssignedAt,
		&d.Status, &d.RequestedAt)
	return d, err
}

type createDRTRequest struct {
	Pickup       latLon `json:"pickup"`
	Dropoff      latLon `json:"dropoff"`
	PickupLabel  string `json:"pickup_label"`
	DropoffLabel string `json:"dropoff_label"`
	Passengers   int    `json:"passengers"`
}

type latLon struct {
	Lat float64 `json:"lat"`
	Lon float64 `json:"lon"`
}

// floatValue dereferences a nullable coordinate (nil → null in the event payload).
func floatValue(f *float64) any {
	if f == nil {
		return nil
	}
	return *f
}

// CreateDRTRequest handles POST /v1/drt/requests (Keycloak JWT). Creates the
// row in citizen.drt_requests and publishes drt.requested (SPEC §3.3) via the
// Dapr pubsub building block (or direct Kafka fallback).
func (h *Handler) CreateDRTRequest(w http.ResponseWriter, r *http.Request) {
	if !h.requireDB(w) {
		return
	}
	var req createDRTRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
		return
	}
	if req.Pickup.Lat == 0 && req.Pickup.Lon == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "pickup {lat,lon} is required"})
		return
	}
	if req.Passengers < 0 || req.Passengers > 20 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "passengers must be between 1 and 20"})
		return
	}
	if req.Passengers == 0 {
		req.Passengers = 1
	}
	sub := auth.Subject(r.Context())
	if sub == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "authenticated subject required"})
		return
	}

	d, err := scanDRT(h.db.QueryRow(r.Context(), `
		INSERT INTO citizen.drt_requests (user_sub, pickup, dropoff, status, pickup_label, dropoff_label, passengers)
		VALUES ($1, ST_SetSRID(ST_MakePoint($2, $3), 4326), ST_SetSRID(ST_MakePoint($4, $5), 4326), 'requested', $6, $7, $8)
		RETURNING `+drtCols,
		sub, req.Pickup.Lon, req.Pickup.Lat, req.Dropoff.Lon, req.Dropoff.Lat,
		req.PickupLabel, req.DropoffLabel, req.Passengers))
	if err != nil {
		h.internal(w, "create drt request", err)
		return
	}

	// drt.requested payload per packages/events/schemas/drt.requested.json:
	// nested pickup/dropoff {lat,lon} objects + requested_at.
	if err := h.pub.Publish(r.Context(), "drt.requested", map[string]any{
		"request_id": d.ID,
		"user_sub":   d.UserSub,
		"pickup": map[string]any{
			"lat": floatValue(d.PickupLat),
			"lon": floatValue(d.PickupLon),
		},
		"dropoff": map[string]any{
			"lat": floatValue(d.DropoffLat),
			"lon": floatValue(d.DropoffLon),
		},
		"requested_at": d.RequestedAt.UTC().Format(time.RFC3339),
	}); err != nil {
		h.log.Error("failed to publish drt.requested", zap.Error(err))
	}
	writeJSON(w, http.StatusCreated, d)
}

// ListDRTRequests handles GET /v1/drt/requests — own requests for the
// authenticated subject, or all when user_sub is supplied by an operator.
func (h *Handler) ListDRTRequests(w http.ResponseWriter, r *http.Request) {
	if !h.requireDB(w) {
		return
	}
	// The ?user_sub= override is gated behind the operator realm role; plain
	// users always see their own requests regardless of the query parameter.
	userSub := r.URL.Query().Get("user_sub")
	if userSub == "" || !auth.HasRole(r.Context(), "operator") {
		userSub = auth.Subject(r.Context())
	}
	if userSub == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "user_sub required"})
		return
	}
	rows, err := h.db.Query(r.Context(), `
		SELECT `+drtCols+` FROM citizen.drt_requests
		WHERE user_sub = $1 ORDER BY requested_at DESC LIMIT 100`, userSub)
	if err != nil {
		h.internal(w, "list drt requests", err)
		return
	}
	defer rows.Close()

	requests := []DRTRequest{}
	for rows.Next() {
		d, err := scanDRT(rows)
		if err != nil {
			h.internal(w, "scan drt request", err)
			return
		}
		requests = append(requests, d)
	}
	if err := rows.Err(); err != nil {
		h.internal(w, "iterate drt requests", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"requests": requests})
}

// CancelDRTRequest handles POST /v1/drt/requests/{id}/cancel (Keycloak JWT).
// Only requests still in 'requested' or 'assigned' status can be cancelled.
func (h *Handler) CancelDRTRequest(w http.ResponseWriter, r *http.Request) {
	if !h.requireDB(w) {
		return
	}
	id := chi.URLParam(r, "id")

	var status, userSub string
	err := h.db.QueryRow(r.Context(),
		`SELECT status, user_sub FROM citizen.drt_requests WHERE id = $1`, id).Scan(&status, &userSub)
	if errors.Is(err, pgx.ErrNoRows) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "request not found"})
		return
	}
	if err != nil {
		h.internal(w, "load drt request", err)
		return
	}
	// Ownership enforcement (mirrors GetDRTRequest): only the rider who
	// created the request may cancel it, unless the caller carries the
	// operator or platform-admin realm role. 404 (not 403/409) so request
	// existence and status are not leaked to non-owners.
	if userSub != auth.Subject(r.Context()) &&
		!auth.HasAnyRole(r.Context(), "operator", "platform-admin") {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "request not found"})
		return
	}
	if status != "requested" && status != "assigned" {
		writeJSON(w, http.StatusConflict, map[string]string{
			"error": "request cannot be cancelled in status " + status,
		})
		return
	}

	d, err := scanDRT(h.db.QueryRow(r.Context(), `
		UPDATE citizen.drt_requests SET status = 'cancelled'
		WHERE id = $1 AND status IN ('requested','assigned')
		RETURNING `+drtCols, id))
	if err != nil {
		h.internal(w, "cancel drt request", err)
		return
	}
	writeJSON(w, http.StatusOK, d)
}

// GetDRTRequest handles GET /v1/drt/requests/{id} (Keycloak JWT). DRT
// requests contain citizen PII: callers may only read their own request
// unless they carry the operator or platform-admin realm role.
func (h *Handler) GetDRTRequest(w http.ResponseWriter, r *http.Request) {
	if !h.requireDB(w) {
		return
	}
	id := chi.URLParam(r, "id")
	d, err := scanDRT(h.db.QueryRow(r.Context(),
		`SELECT `+drtCols+` FROM citizen.drt_requests WHERE id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "request not found"})
		return
	}
	if err != nil {
		h.internal(w, "get drt request", err)
		return
	}
	if d.UserSub != auth.Subject(r.Context()) &&
		!auth.HasAnyRole(r.Context(), "operator", "platform-admin") {
		// 404 (not 403) so request existence is not leaked to non-owners.
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "request not found"})
		return
	}
	writeJSON(w, http.StatusOK, d)
}

// ErrDRTNotAssignable marks the "no row in requested status" outcome.
var ErrDRTNotAssignable = errors.New("drt request not assignable")

// AssignDRT performs the requested→assigned transition, shared by the
// operator endpoint and the drt.requested auto-assignment consumer
// (BUSINESS_LOGIC_AUDIT §13: the assigned status was unreachable).
func AssignDRT(ctx context.Context, db DB, requestID, vehicleID, driverSub string) error {
	var driver any
	if driverSub != "" {
		driver = driverSub
	}
	tag, err := db.Exec(ctx, `
		UPDATE citizen.drt_requests
		SET status = 'assigned', vehicle_id = $2, driver_sub = $3, assigned_at = now()
		WHERE id = $1 AND status = 'requested'`, requestID, vehicleID, driver)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrDRTNotAssignable
	}
	return nil
}

// AssignDRTRequest handles POST /v1/drt/requests/{id}/assign (Keycloak JWT,
// operator): manual assignment of a requested ride to a vehicle. The
// vehicle must exist (0005 S4 FK → 422); only 'requested' rides can be
// assigned (409 otherwise). Publishes drt.assigned.
func (h *Handler) AssignDRTRequest(w http.ResponseWriter, r *http.Request) {
	if !h.requireDB(w) {
		return
	}
	id := chi.URLParam(r, "id")
	var req struct {
		VehicleID string `json:"vehicle_id"`
		DriverSub string `json:"driver_sub"`
	}
	if err := decodeJSON(w, r, &req); err != nil || req.VehicleID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "body must include \"vehicle_id\""})
		return
	}
	err := AssignDRT(r.Context(), h.db, id, req.VehicleID, req.DriverSub)
	if errors.Is(err, ErrDRTNotAssignable) {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "request not found or not in requested status"})
		return
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23503" {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": "vehicle_id does not reference a known vehicle"})
		return
	}
	if err != nil {
		h.internal(w, "assign drt request", err)
		return
	}
	d, err := scanDRT(h.db.QueryRow(r.Context(),
		`SELECT `+drtCols+` FROM citizen.drt_requests WHERE id = $1`, id))
	if err != nil {
		h.internal(w, "reload drt request", err)
		return
	}
	if err := h.pub.Publish(r.Context(), "drt.assigned", map[string]any{
		"request_id":  d.ID,
		"user_sub":    d.UserSub,
		"vehicle_id":  d.VehicleID,
		"driver_sub":  d.DriverSub,
		"assigned_at": time.Now().UTC().Format(time.RFC3339),
	}); err != nil {
		h.log.Error("failed to publish drt.assigned", zap.Error(err))
	}
	writeJSON(w, http.StatusOK, d)
}

// ProgressDRTRequest handles POST /v1/drt/requests/{id}/start and
// .../{id}/complete (Keycloak JWT, driver|operator): assigned → enroute →
// completed, the previously unreachable end of the DRT state machine.
func (h *Handler) ProgressDRTRequest(w http.ResponseWriter, r *http.Request) {
	if !h.requireDB(w) {
		return
	}
	id := chi.URLParam(r, "id")
	var from, to string
	switch {
	case strings.HasSuffix(r.URL.Path, "/start"):
		from, to = "assigned", "enroute"
	case strings.HasSuffix(r.URL.Path, "/complete"):
		from, to = "enroute", "completed"
	default:
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown DRT progression"})
		return
	}
	d, err := scanDRT(h.db.QueryRow(r.Context(), `
		UPDATE citizen.drt_requests SET status = $3
		WHERE id = $1 AND status = $2
		RETURNING `+drtCols, id, from, to))
	if errors.Is(err, pgx.ErrNoRows) {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "request not found or not in " + from + " status"})
		return
	}
	if err != nil {
		h.internal(w, "progress drt request", err)
		return
	}
	writeJSON(w, http.StatusOK, d)
}
