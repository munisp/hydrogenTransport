package handlers

import (
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// WorkOrder mirrors infra.work_orders (depot-management module). bus_id /
// prediction_id / assignee / started_at come from migration 0005 (S12) and
// make the predictive-maintenance → work-order loop expressible.
type WorkOrder struct {
	ID           string     `json:"id"`
	Title        string     `json:"title"`
	Description  string     `json:"description"`
	AssetRef     string     `json:"asset_ref"`
	Status       string     `json:"status"`
	BusID        *string    `json:"bus_id,omitempty"`
	PredictionID *string    `json:"prediction_id,omitempty"`
	Assignee     *string    `json:"assignee,omitempty"`
	StartedAt    *time.Time `json:"started_at,omitempty"`
	OpenedAt     time.Time  `json:"opened_at"`
	ClosedAt     *time.Time `json:"closed_at,omitempty"`
}

const workOrderCols = `id, title, description, asset_ref, status, bus_id, prediction_id,
	assignee, started_at, opened_at, closed_at`

func scanWorkOrder(row pgx.Row) (WorkOrder, error) {
	var o WorkOrder
	err := row.Scan(&o.ID, &o.Title, &o.Description, &o.AssetRef, &o.Status,
		&o.BusID, &o.PredictionID, &o.Assignee, &o.StartedAt, &o.OpenedAt, &o.ClosedAt)
	return o, err
}

// workOrderTransitions is the depot work-order lifecycle
// (BUSINESS_LOGIC_AUDIT §10: open→closed only). closed is terminal; any
// non-closed status can close.
var workOrderTransitions = map[string]map[string]bool{
	"open":        {"assigned": true, "in_progress": true, "closed": true},
	"assigned":    {"in_progress": true, "on_hold": true, "open": true, "closed": true},
	"in_progress": {"on_hold": true, "closed": true},
	"on_hold":     {"in_progress": true, "closed": true},
	"closed":      {},
}

// DepotBay mirrors infra.depot_bays (depot-management module).
type DepotBay struct {
	ID         string  `json:"id"`
	Depot      string  `json:"depot"`
	Label      string  `json:"label"`
	Kind       string  `json:"kind"`
	OccupiedBy *string `json:"occupied_by"`
	Status     string  `json:"status"`
}

const depotBayCols = `id, depot, label, kind, occupied_by, status`

func scanDepotBay(row pgx.Row) (DepotBay, error) {
	var b DepotBay
	err := row.Scan(&b.ID, &b.Depot, &b.Label, &b.Kind, &b.OccupiedBy, &b.Status)
	return b, err
}

// ListDepotBays handles GET /v1/depot/bays?depot=&status=.
func (h *Handler) ListDepotBays(w http.ResponseWriter, r *http.Request) {
	query := `SELECT ` + depotBayCols + ` FROM infra.depot_bays`
	args := []any{}
	where := ""
	if depot := r.URL.Query().Get("depot"); depot != "" {
		args = append(args, depot)
		where += fmt.Sprintf(" AND depot = $%d", len(args))
	}
	if status := r.URL.Query().Get("status"); status != "" {
		args = append(args, status)
		where += fmt.Sprintf(" AND status = $%d", len(args))
	}
	if where != "" {
		query += ` WHERE ` + where[len(" AND "):]
	}
	query += ` ORDER BY depot, label`

	rows, err := h.db.Query(r.Context(), query, args...)
	if err != nil {
		h.internal(w, "list depot bays", err)
		return
	}
	defer rows.Close()

	bays := []DepotBay{}
	for rows.Next() {
		b, err := scanDepotBay(rows)
		if err != nil {
			h.internal(w, "scan depot bay", err)
			return
		}
		bays = append(bays, b)
	}
	if err := rows.Err(); err != nil {
		h.internal(w, "iterate depot bays", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"bays": bays})
}

// AssignDepotBay handles POST /v1/depot/bays/{id}/assign {bus_id} (Keycloak
// JWT, operator): parks a bus in a free bay — sets occupied_by and flips the
// bay to 'occupied' (BUSINESS_LOGIC_AUDIT §10: occupied_by was never set).
// 409 when the bay is not free, 422 for an unknown bus.
func (h *Handler) AssignDepotBay(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var req struct {
		BusID string `json:"bus_id"`
	}
	if err := decodeJSON(w, r, &req); err != nil || req.BusID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "body must include \"bus_id\""})
		return
	}
	b, err := scanDepotBay(h.db.QueryRow(r.Context(), `
		UPDATE infra.depot_bays SET occupied_by = $2, status = 'occupied'
		WHERE id = $1 AND status = 'free'
		RETURNING `+depotBayCols, id, req.BusID))
	if errors.Is(err, pgx.ErrNoRows) {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "bay not found or not free"})
		return
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23503" {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": "bus_id does not reference a known vehicle"})
		return
	}
	if err != nil {
		h.internal(w, "assign depot bay", err)
		return
	}
	writeJSON(w, http.StatusOK, b)
}

// ReleaseDepotBay handles POST /v1/depot/bays/{id}/release (Keycloak JWT,
// operator): the bus leaves — occupied_by clears and the bay becomes free.
func (h *Handler) ReleaseDepotBay(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	b, err := scanDepotBay(h.db.QueryRow(r.Context(), `
		UPDATE infra.depot_bays SET occupied_by = NULL, status = 'free'
		WHERE id = $1 AND status = 'occupied'
		RETURNING `+depotBayCols, id))
	if errors.Is(err, pgx.ErrNoRows) {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "bay not found or not occupied"})
		return
	}
	if err != nil {
		h.internal(w, "release depot bay", err)
		return
	}
	writeJSON(w, http.StatusOK, b)
}

// ListWorkOrders handles GET /v1/depot/work-orders?status=&bus_id=.
func (h *Handler) ListWorkOrders(w http.ResponseWriter, r *http.Request) {
	query := `SELECT ` + workOrderCols + ` FROM infra.work_orders`
	args := []any{}
	conds := ""
	if status := r.URL.Query().Get("status"); status != "" {
		args = append(args, status)
		conds += fmt.Sprintf(" AND status = $%d", len(args))
	}
	if busID := r.URL.Query().Get("bus_id"); busID != "" {
		args = append(args, busID)
		conds += fmt.Sprintf(" AND bus_id = $%d", len(args))
	}
	if conds != "" {
		query += ` WHERE ` + conds[len(" AND "):]
	}
	query += ` ORDER BY opened_at DESC LIMIT 200`

	rows, err := h.db.Query(r.Context(), query, args...)
	if err != nil {
		h.internal(w, "list work orders", err)
		return
	}
	defer rows.Close()

	orders := []WorkOrder{}
	for rows.Next() {
		o, err := scanWorkOrder(rows)
		if err != nil {
			h.internal(w, "scan work order", err)
			return
		}
		orders = append(orders, o)
	}
	if err := rows.Err(); err != nil {
		h.internal(w, "iterate work orders", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"work_orders": orders})
}

type createWorkOrderRequest struct {
	Title        string  `json:"title"`
	Description  string  `json:"description"`
	AssetRef     string  `json:"asset_ref"`
	BusID        *string `json:"bus_id"`
	PredictionID *string `json:"prediction_id"`
	Assignee     *string `json:"assignee"`
}

// CreateWorkOrder handles POST /v1/depot/work-orders (Keycloak JWT). Accepts
// optional bus / maintenance-prediction linkage (0005 S12); a prediction-linked
// order is deduplicated by the open-prediction unique index (0007) so the
// maintenance.predicted consumer can retry safely.
func (h *Handler) CreateWorkOrder(w http.ResponseWriter, r *http.Request) {
	var req createWorkOrderRequest
	if err := decodeJSON(w, r, &req); err != nil || req.Title == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "body must include \"title\""})
		return
	}
	status := "open"
	if req.Assignee != nil && *req.Assignee != "" {
		status = "assigned"
	}
	o, err := scanWorkOrder(h.db.QueryRow(r.Context(), `
		INSERT INTO infra.work_orders (title, description, asset_ref, status, bus_id, prediction_id, assignee)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		RETURNING `+workOrderCols,
		req.Title, req.Description, req.AssetRef, status, req.BusID, req.PredictionID, req.Assignee))
	if err != nil {
		var pgErr *pgconn.PgError
		switch {
		case errors.As(err, &pgErr) && pgErr.Code == "23503":
			writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": "bus_id or prediction_id does not reference a known vehicle/prediction"})
		case errors.As(err, &pgErr) && pgErr.Code == "23505":
			writeJSON(w, http.StatusConflict, map[string]string{"error": "an open work order already exists for this prediction"})
		default:
			h.internal(w, "create work order", err)
		}
		return
	}
	writeJSON(w, http.StatusCreated, o)
}

type transitionWorkOrderRequest struct {
	Assignee *string `json:"assignee"`
}

// transitionWorkOrder applies one lifecycle transition, stamping the
// bookkeeping columns for the target status.
func (h *Handler) transitionWorkOrder(w http.ResponseWriter, r *http.Request, target string) {
	id := chi.URLParam(r, "id")
	var req transitionWorkOrderRequest
	if target == "assigned" {
		if err := decodeJSON(w, r, &req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
			return
		}
	}

	var current string
	err := h.db.QueryRow(r.Context(),
		`SELECT status FROM infra.work_orders WHERE id = $1`, id).Scan(&current)
	if errors.Is(err, pgx.ErrNoRows) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "work order not found"})
		return
	}
	if err != nil {
		h.internal(w, "load work order", err)
		return
	}
	if current == target {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "work order already in status " + target})
		return
	}
	if !workOrderTransitions[current][target] {
		writeJSON(w, http.StatusConflict, map[string]string{
			"error": "invalid status transition from " + current + " to " + target,
		})
		return
	}

	setClause := `status = $2`
	args := []any{id, target}
	switch target {
	case "assigned":
		if req.Assignee == nil || *req.Assignee == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "assignee is required when assigning a work order"})
			return
		}
		setClause = `status = $2, assignee = $3`
		args = append(args, *req.Assignee)
	case "in_progress":
		setClause = `status = $2, started_at = COALESCE(started_at, now())`
	case "closed":
		setClause = `status = $2, closed_at = now()`
	}

	o, err := scanWorkOrder(h.db.QueryRow(r.Context(), `
		UPDATE infra.work_orders SET `+setClause+`
		WHERE id = $1
		RETURNING `+workOrderCols, args...))
	if err != nil {
		h.internal(w, "transition work order", err)
		return
	}
	writeJSON(w, http.StatusOK, o)
}

// AssignWorkOrder handles POST /v1/depot/work-orders/{id}/assign {assignee}.
func (h *Handler) AssignWorkOrder(w http.ResponseWriter, r *http.Request) {
	h.transitionWorkOrder(w, r, "assigned")
}

// StartWorkOrder handles POST /v1/depot/work-orders/{id}/start.
func (h *Handler) StartWorkOrder(w http.ResponseWriter, r *http.Request) {
	h.transitionWorkOrder(w, r, "in_progress")
}

// HoldWorkOrder handles POST /v1/depot/work-orders/{id}/hold.
func (h *Handler) HoldWorkOrder(w http.ResponseWriter, r *http.Request) {
	h.transitionWorkOrder(w, r, "on_hold")
}

// CloseWorkOrder handles POST /v1/depot/work-orders/{id}/close (Keycloak JWT).
func (h *Handler) CloseWorkOrder(w http.ResponseWriter, r *http.Request) {
	h.transitionWorkOrder(w, r, "closed")
}
