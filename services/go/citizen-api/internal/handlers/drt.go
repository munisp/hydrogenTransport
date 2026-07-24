package handlers

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"go.uber.org/zap"

	"github.com/munisp/hydrogenTransport/services/go/citizen-api/internal/auth"
)

// DRTRequest mirrors citizen.drt_requests (demand-responsive module).
type DRTRequest struct {
	ID          string    `json:"id"`
	UserSub     string    `json:"user_sub"`
	PickupLat   *float64  `json:"pickup_lat,omitempty"`
	PickupLon   *float64  `json:"pickup_lon,omitempty"`
	DropoffLat  *float64  `json:"dropoff_lat,omitempty"`
	DropoffLon  *float64  `json:"dropoff_lon,omitempty"`
	Status      string    `json:"status"`
	RequestedAt time.Time `json:"requested_at"`
}

const drtCols = `id, user_sub,
	ST_Y(pickup)::float8, ST_X(pickup)::float8,
	ST_Y(dropoff)::float8, ST_X(dropoff)::float8,
	status, requested_at`

func scanDRT(row pgx.Row) (DRTRequest, error) {
	var d DRTRequest
	err := row.Scan(&d.ID, &d.UserSub, &d.PickupLat, &d.PickupLon, &d.DropoffLat, &d.DropoffLon, &d.Status, &d.RequestedAt)
	return d, err
}

type createDRTRequest struct {
	Pickup  latLon `json:"pickup"`
	Dropoff latLon `json:"dropoff"`
}

type latLon struct {
	Lat float64 `json:"lat"`
	Lon float64 `json:"lon"`
}

// CreateDRTRequest handles POST /v1/drt/requests (Keycloak JWT). Creates the
// row in citizen.drt_requests and publishes drt.requested (SPEC §3.3) via the
// Dapr pubsub building block (or direct Kafka fallback).
func (h *Handler) CreateDRTRequest(w http.ResponseWriter, r *http.Request) {
	if !h.requireDB(w) {
		return
	}
	var req createDRTRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
		return
	}
	if req.Pickup.Lat == 0 && req.Pickup.Lon == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "pickup {lat,lon} is required"})
		return
	}
	sub := auth.Subject(r.Context())
	if sub == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "authenticated subject required"})
		return
	}

	d, err := scanDRT(h.db.QueryRow(r.Context(), `
		INSERT INTO citizen.drt_requests (user_sub, pickup, dropoff, status)
		VALUES ($1, ST_SetSRID(ST_MakePoint($2, $3), 4326), ST_SetSRID(ST_MakePoint($4, $5), 4326), 'requested')
		RETURNING `+drtCols,
		sub, req.Pickup.Lon, req.Pickup.Lat, req.Dropoff.Lon, req.Dropoff.Lat))
	if err != nil {
		h.internal(w, "create drt request", err)
		return
	}

	if err := h.pub.Publish(r.Context(), "drt.requested", map[string]any{
		"request_id":  d.ID,
		"user_sub":    d.UserSub,
		"pickup_lat":  d.PickupLat,
		"pickup_lon":  d.PickupLon,
		"dropoff_lat": d.DropoffLat,
		"dropoff_lon": d.DropoffLon,
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
	userSub := r.URL.Query().Get("user_sub")
	if userSub == "" {
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

	var status string
	err := h.db.QueryRow(r.Context(),
		`SELECT status FROM citizen.drt_requests WHERE id = $1`, id).Scan(&status)
	if errors.Is(err, pgx.ErrNoRows) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "request not found"})
		return
	}
	if err != nil {
		h.internal(w, "load drt request", err)
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

// GetDRTRequest handles GET /v1/drt/requests/{id}.
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
	writeJSON(w, http.StatusOK, d)
}
