package handlers

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
)

// WorkOrder mirrors infra.work_orders (depot-management module).
type WorkOrder struct {
	ID          string     `json:"id"`
	Title       string     `json:"title"`
	Description string     `json:"description"`
	AssetRef    string     `json:"asset_ref"`
	Status      string     `json:"status"`
	OpenedAt    time.Time  `json:"opened_at"`
	ClosedAt    *time.Time `json:"closed_at,omitempty"`
}

const workOrderCols = `id, title, description, asset_ref, status, opened_at, closed_at`

// DepotBay mirrors infra.depot_bays (depot-management module).
type DepotBay struct {
	ID         string  `json:"id"`
	Depot      string  `json:"depot"`
	Label      string  `json:"label"`
	Kind       string  `json:"kind"`
	OccupiedBy *string `json:"occupied_by"`
	Status     string  `json:"status"`
}

// ListDepotBays handles GET /v1/depot/bays?depot=&status=.
func (h *Handler) ListDepotBays(w http.ResponseWriter, r *http.Request) {
	query := `SELECT id, depot, label, kind, occupied_by, status FROM infra.depot_bays`
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
		var b DepotBay
		if err := rows.Scan(&b.ID, &b.Depot, &b.Label, &b.Kind, &b.OccupiedBy, &b.Status); err != nil {
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

// ListWorkOrders handles GET /v1/depot/work-orders?status=.
func (h *Handler) ListWorkOrders(w http.ResponseWriter, r *http.Request) {
	query := `SELECT ` + workOrderCols + ` FROM infra.work_orders`
	args := []any{}
	if status := r.URL.Query().Get("status"); status != "" {
		query += ` WHERE status = $1`
		args = append(args, status)
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
		var o WorkOrder
		if err := rows.Scan(&o.ID, &o.Title, &o.Description, &o.AssetRef, &o.Status, &o.OpenedAt, &o.ClosedAt); err != nil {
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
	Title       string `json:"title"`
	Description string `json:"description"`
	AssetRef    string `json:"asset_ref"`
}

// CreateWorkOrder handles POST /v1/depot/work-orders (Keycloak JWT).
func (h *Handler) CreateWorkOrder(w http.ResponseWriter, r *http.Request) {
	var req createWorkOrderRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Title == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "body must include \"title\""})
		return
	}
	var o WorkOrder
	err := h.db.QueryRow(r.Context(), `
		INSERT INTO infra.work_orders (title, description, asset_ref)
		VALUES ($1, $2, $3)
		RETURNING `+workOrderCols, req.Title, req.Description, req.AssetRef).
		Scan(&o.ID, &o.Title, &o.Description, &o.AssetRef, &o.Status, &o.OpenedAt, &o.ClosedAt)
	if err != nil {
		h.internal(w, "create work order", err)
		return
	}
	writeJSON(w, http.StatusCreated, o)
}

// CloseWorkOrder handles POST /v1/depot/work-orders/{id}/close (Keycloak JWT).
func (h *Handler) CloseWorkOrder(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var o WorkOrder
	err := h.db.QueryRow(r.Context(), `
		UPDATE infra.work_orders SET status = 'closed', closed_at = now()
		WHERE id = $1 AND status <> 'closed'
		RETURNING `+workOrderCols, id).
		Scan(&o.ID, &o.Title, &o.Description, &o.AssetRef, &o.Status, &o.OpenedAt, &o.ClosedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "work order not found or already closed"})
		return
	}
	if err != nil {
		h.internal(w, "close work order", err)
		return
	}
	writeJSON(w, http.StatusOK, o)
}
