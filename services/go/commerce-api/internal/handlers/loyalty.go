package handlers

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	auth "github.com/munisp/hydrogenTransport/packages/go-auth"
)

// LoyaltyBalance is a rider's rewards balance (loyalty-marketplace module).
type LoyaltyBalance struct {
	UserSub   string    `json:"user_sub"`
	Points    int       `json:"points"`
	UpdatedAt time.Time `json:"updated_at"`
}

// GetLoyaltyBalance handles GET /v1/loyalty/balance (Keycloak JWT; lazily
// creates the account).
func (h *Handler) GetLoyaltyBalance(w http.ResponseWriter, r *http.Request) {
	sub := auth.Subject(r.Context())
	if sub == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "authenticated subject required"})
		return
	}
	var b LoyaltyBalance
	err := h.db.QueryRow(r.Context(), `
		INSERT INTO commerce.loyalty_accounts (user_sub) VALUES ($1)
		ON CONFLICT (user_sub) DO UPDATE SET user_sub = EXCLUDED.user_sub
		RETURNING user_sub, points, updated_at`, sub).
		Scan(&b.UserSub, &b.Points, &b.UpdatedAt)
	if err != nil {
		h.internal(w, "loyalty balance", err)
		return
	}
	writeJSON(w, http.StatusOK, b)
}

// Offer is a marketplace reward offer.
type Offer struct {
	ID          string    `json:"id"`
	Title       string    `json:"title"`
	Description string    `json:"description"`
	Partner     string    `json:"partner"`
	CostPoints  int       `json:"cost_points"`
	Active      bool      `json:"active"`
	CreatedAt   time.Time `json:"created_at"`
}

const offerCols = `id, title, description, partner, cost_points, active, created_at`

// ListOffers handles GET /v1/marketplace/offers.
func (h *Handler) ListOffers(w http.ResponseWriter, r *http.Request) {
	rows, err := h.db.Query(r.Context(),
		`SELECT `+offerCols+` FROM commerce.marketplace_offers ORDER BY created_at DESC LIMIT 200`)
	if err != nil {
		h.internal(w, "list offers", err)
		return
	}
	defer rows.Close()

	offers := []Offer{}
	for rows.Next() {
		var o Offer
		if err := rows.Scan(&o.ID, &o.Title, &o.Description, &o.Partner, &o.CostPoints, &o.Active, &o.CreatedAt); err != nil {
			h.internal(w, "scan offer", err)
			return
		}
		offers = append(offers, o)
	}
	if err := rows.Err(); err != nil {
		h.internal(w, "iterate offers", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"offers": offers})
}

type createOfferRequest struct {
	Title       string `json:"title"`
	Description string `json:"description"`
	Partner     string `json:"partner"`
	CostPoints  int    `json:"cost_points"`
}

// CreateOffer handles POST /v1/marketplace/offers (Keycloak JWT).
func (h *Handler) CreateOffer(w http.ResponseWriter, r *http.Request) {
	var req createOfferRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Title == "" || req.CostPoints <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "title and positive cost_points are required"})
		return
	}
	var o Offer
	err := h.db.QueryRow(r.Context(), `
		INSERT INTO commerce.marketplace_offers (title, description, partner, cost_points)
		VALUES ($1, $2, $3, $4) RETURNING `+offerCols,
		req.Title, req.Description, req.Partner, req.CostPoints).
		Scan(&o.ID, &o.Title, &o.Description, &o.Partner, &o.CostPoints, &o.Active, &o.CreatedAt)
	if err != nil {
		h.internal(w, "create offer", err)
		return
	}
	writeJSON(w, http.StatusCreated, o)
}

type redeemRequest struct {
	OfferID string `json:"offer_id"`
}

// RedeemOffer handles POST /v1/loyalty/redeem (Keycloak JWT). Atomically
// deducts the offer's points from the caller's balance.
func (h *Handler) RedeemOffer(w http.ResponseWriter, r *http.Request) {
	sub := auth.Subject(r.Context())
	if sub == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "authenticated subject required"})
		return
	}
	var req redeemRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.OfferID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "offer_id is required"})
		return
	}

	tx, err := h.db.Begin(r.Context())
	if err != nil {
		h.internal(w, "begin redeem tx", err)
		return
	}
	defer func() { _ = tx.Rollback(r.Context()) }()

	var cost int
	err = tx.QueryRow(r.Context(), `
		SELECT cost_points FROM commerce.marketplace_offers WHERE id = $1 AND active FOR UPDATE`, req.OfferID).
		Scan(&cost)
	if errors.Is(err, pgx.ErrNoRows) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "offer not found or inactive"})
		return
	}
	if err != nil {
		h.internal(w, "load offer", err)
		return
	}

	if _, err := tx.Exec(r.Context(), `
		INSERT INTO commerce.loyalty_accounts (user_sub) VALUES ($1)
		ON CONFLICT (user_sub) DO NOTHING`, sub); err != nil {
		h.internal(w, "ensure loyalty account", err)
		return
	}

	var balance int
	if err := tx.QueryRow(r.Context(), `
		UPDATE commerce.loyalty_accounts SET points = points - $2, updated_at = now()
		WHERE user_sub = $1 AND points >= $2
		RETURNING points`, sub, cost).Scan(&balance); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "insufficient loyalty points"})
			return
		}
		h.internal(w, "deduct points", err)
		return
	}

	if err := tx.Commit(r.Context()); err != nil {
		h.internal(w, "commit redeem", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"redeemed_offer_id": req.OfferID,
		"points_spent":      cost,
		"remaining_points":  balance,
	})
}
