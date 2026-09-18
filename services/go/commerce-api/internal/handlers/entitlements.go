package handlers

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	auth "github.com/munisp/hydrogenTransport/packages/go-auth"
)

// FareProduct mirrors commerce.fare_products (0009 G1, Wave-6 A2-01): a
// sellable fare product — a period pass, a percentage concession, or a
// free-travel entitlement class.
type FareProduct struct {
	ID           string    `json:"id"`
	Code         string    `json:"code"`
	Kind         string    `json:"kind"` // pass|discount|free
	DiscountPct  *int      `json:"discount_pct,omitempty"`
	PriceMinor   int64     `json:"price_minor"`
	DurationDays int       `json:"duration_days"`
	Description  string    `json:"description"`
	Active       bool      `json:"active"`
	CreatedAt    time.Time `json:"created_at"`
}

const fareProductCols = `id, code, kind, discount_pct, price_minor, duration_days, description, active, created_at`

func scanFareProduct(row pgx.Row) (FareProduct, error) {
	var p FareProduct
	err := row.Scan(&p.ID, &p.Code, &p.Kind, &p.DiscountPct, &p.PriceMinor,
		&p.DurationDays, &p.Description, &p.Active, &p.CreatedAt)
	return p, err
}

// CreateFareProduct handles POST /v1/fare-products (operator/platform-admin).
func (h *Handler) CreateFareProduct(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Code         string `json:"code"`
		Kind         string `json:"kind"`
		DiscountPct  *int   `json:"discount_pct"`
		PriceMinor   int64  `json:"price_minor"`
		DurationDays int    `json:"duration_days"`
		Description  string `json:"description"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil || req.Code == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "body must include \"code\""})
		return
	}
	switch req.Kind {
	case "pass", "free":
		// no percentage
	case "discount":
		if req.DiscountPct == nil || *req.DiscountPct <= 0 || *req.DiscountPct > 100 {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "discount products require 0 < discount_pct <= 100"})
			return
		}
	default:
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "kind must be pass, discount or free"})
		return
	}
	if req.DurationDays <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "duration_days must be positive"})
		return
	}
	if req.PriceMinor < 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "price_minor must be >= 0"})
		return
	}
	p, err := scanFareProduct(h.db.QueryRow(r.Context(), `
		INSERT INTO commerce.fare_products (code, kind, discount_pct, price_minor, duration_days, description)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING `+fareProductCols,
		req.Code, req.Kind, req.DiscountPct, req.PriceMinor, req.DurationDays, req.Description))
	if err != nil {
		h.internal(w, "create fare product", err)
		return
	}
	writeJSON(w, http.StatusCreated, p)
}

// ListFareProducts handles GET /v1/fare-products?active= (any authenticated
// caller — the catalog is public tariff information).
func (h *Handler) ListFareProducts(w http.ResponseWriter, r *http.Request) {
	query := `SELECT ` + fareProductCols + ` FROM commerce.fare_products`
	args := []any{}
	if r.URL.Query().Get("active") == "true" {
		query += ` WHERE active`
	}
	query += ` ORDER BY code`
	rows, err := h.db.Query(r.Context(), query, args...)
	if err != nil {
		h.internal(w, "list fare products", err)
		return
	}
	defer rows.Close()
	products := []FareProduct{}
	for rows.Next() {
		p, err := scanFareProduct(rows)
		if err != nil {
			h.internal(w, "scan fare product", err)
			return
		}
		products = append(products, p)
	}
	if err := rows.Err(); err != nil {
		h.internal(w, "iterate fare products", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"fare_products": products})
}

// Entitlement mirrors commerce.rider_entitlements (0009 G1).
type Entitlement struct {
	ID        string    `json:"id"`
	RiderSub  string    `json:"rider_sub"`
	ProductID string    `json:"product_id"`
	ValidFrom time.Time `json:"valid_from"`
	ValidTo   time.Time `json:"valid_to"`
	CreatedBy string    `json:"created_by"`
	CreatedAt time.Time `json:"created_at"`
}

const entitlementCols = `id, rider_sub, product_id, valid_from, valid_to, created_by, created_at`

// GrantEntitlement handles POST /v1/entitlements (operator/platform-admin):
// grants a rider a fare product for its duration (or an explicit window).
// Selling the product (collecting price_minor) is a separate fare payment —
// the entitlement grant itself moves no money.
func (h *Handler) GrantEntitlement(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RiderSub  string     `json:"rider_sub"`
		ProductID string     `json:"product_id"`
		ValidFrom *time.Time `json:"valid_from"`
		ValidTo   *time.Time `json:"valid_to"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil || req.RiderSub == "" || req.ProductID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "body must include \"rider_sub\" and \"product_id\""})
		return
	}
	// Load the product for its duration default and to 404 unknown products.
	var durationDays int
	if err := h.db.QueryRow(r.Context(),
		`SELECT duration_days FROM commerce.fare_products WHERE id = $1 AND active`, req.ProductID).
		Scan(&durationDays); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown or inactive product_id"})
			return
		}
		h.internal(w, "load fare product", err)
		return
	}
	from := time.Now().UTC()
	if req.ValidFrom != nil {
		from = *req.ValidFrom
	}
	to := from.Add(time.Duration(durationDays) * 24 * time.Hour)
	if req.ValidTo != nil {
		to = *req.ValidTo
	}
	if !to.After(from) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "valid_to must be after valid_from"})
		return
	}
	var e Entitlement
	err := h.db.QueryRow(r.Context(), `
		INSERT INTO commerce.rider_entitlements (rider_sub, product_id, valid_from, valid_to, created_by)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING `+entitlementCols,
		req.RiderSub, req.ProductID, from, to, auth.Subject(r.Context())).
		Scan(&e.ID, &e.RiderSub, &e.ProductID, &e.ValidFrom, &e.ValidTo, &e.CreatedBy, &e.CreatedAt)
	if err != nil {
		h.internal(w, "grant entitlement", err)
		return
	}
	writeJSON(w, http.StatusCreated, e)
}

// ListEntitlements handles GET /v1/entitlements?rider_sub=. Non-operator
// callers are scoped to their own subject (entitlements are personal data).
func (h *Handler) ListEntitlements(w http.ResponseWriter, r *http.Request) {
	rider := r.URL.Query().Get("rider_sub")
	if !auth.HasAnyRole(r.Context(), "operator", "platform-admin") {
		rider = auth.Subject(r.Context())
	}
	query := `SELECT ` + entitlementCols + ` FROM commerce.rider_entitlements`
	args := []any{}
	if rider != "" {
		query += ` WHERE rider_sub = $1`
		args = append(args, rider)
	}
	query += ` ORDER BY valid_to DESC LIMIT 200`
	rows, err := h.db.Query(r.Context(), query, args...)
	if err != nil {
		h.internal(w, "list entitlements", err)
		return
	}
	defer rows.Close()
	entitlements := []Entitlement{}
	for rows.Next() {
		var e Entitlement
		if err := rows.Scan(&e.ID, &e.RiderSub, &e.ProductID, &e.ValidFrom,
			&e.ValidTo, &e.CreatedBy, &e.CreatedAt); err != nil {
			h.internal(w, "scan entitlement", err)
			return
		}
		entitlements = append(entitlements, e)
	}
	if err := rows.Err(); err != nil {
		h.internal(w, "iterate entitlements", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"entitlements": entitlements})
}
