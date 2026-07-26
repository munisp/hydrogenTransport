package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	auth "github.com/munisp/hydrogenTransport/packages/go-auth"
)

// LoyaltyBalance is a rider's rewards balance (loyalty-marketplace module).
type LoyaltyBalance struct {
	RiderSub  string    `json:"rider_sub"`
	Points    int       `json:"points"`
	UpdatedAt time.Time `json:"updated_at"`
}

// GetLoyaltyBalance handles GET /v1/loyalty/balance (Keycloak JWT). Returns
// the real balance from commerce.loyalty_accounts, lazily creating the
// account (0 points) for first-time riders.
func (h *Handler) GetLoyaltyBalance(w http.ResponseWriter, r *http.Request) {
	sub := auth.Subject(r.Context())
	if sub == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "authenticated subject required"})
		return
	}
	var b LoyaltyBalance
	err := h.db.QueryRow(r.Context(), `
		INSERT INTO commerce.loyalty_accounts (rider_sub) VALUES ($1)
		ON CONFLICT (rider_sub) DO UPDATE SET rider_sub = EXCLUDED.rider_sub
		RETURNING rider_sub, points, updated_at`, sub).
		Scan(&b.RiderSub, &b.Points, &b.UpdatedAt)
	if err != nil {
		h.internal(w, "loyalty balance", err)
		return
	}
	writeJSON(w, http.StatusOK, b)
}

// accrueLoyaltyPoints awards loyalty points for a settled fare payment:
// 1 point per full 100 minor units (€1) spent, rounded down. The award is
// idempotent per payment: commerce.loyalty_ledger.ref_id is the payment id
// with a UNIQUE constraint, and the balance increment is applied only when
// the ledger insert actually lands (ON CONFLICT DO NOTHING → 0 rows on
// replay). Both writes share one transaction so a crash cannot record the
// ledger entry without the balance (or vice versa).
func (h *Handler) accrueLoyaltyPoints(ctx context.Context, paymentID, riderSub string, amountMinor int64) error {
	points := amountMinor / 100
	if points <= 0 || riderSub == "" {
		return nil
	}
	tx, err := h.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	tag, err := tx.Exec(ctx, `
		INSERT INTO commerce.loyalty_ledger (id, rider_sub, delta, reason, ref_id)
		VALUES ($1, $2, $3, 'fare_accrual', $4)
		ON CONFLICT (ref_id) DO NOTHING`,
		uuid.NewString(), riderSub, points, paymentID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 1 {
		if _, err := tx.Exec(ctx, `
			INSERT INTO commerce.loyalty_accounts (rider_sub, points)
			VALUES ($1, $2)
			ON CONFLICT (rider_sub) DO UPDATE
			SET points = commerce.loyalty_accounts.points + EXCLUDED.points,
				updated_at = now()`, riderSub, points); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// clawbackLoyaltyPoints reverses the fare-accrual award for a refunded
// payment: a negative ledger entry idempotent on "refund:<payment_id>", and
// a balance decrement floored at 0 (points may already have been spent —
// the ledger keeps the exact audit trail either way).
func (h *Handler) clawbackLoyaltyPoints(ctx context.Context, paymentID, riderSub string, chargedMinor int64) error {
	points := chargedMinor / 100
	if points <= 0 || riderSub == "" {
		return nil
	}
	tx, err := h.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	tag, err := tx.Exec(ctx, `
		INSERT INTO commerce.loyalty_ledger (id, rider_sub, delta, reason, ref_id)
		VALUES ($1, $2, $3, 'refund_clawback', $4)
		ON CONFLICT (ref_id) DO NOTHING`,
		uuid.NewString(), riderSub, -points, "refund:"+paymentID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 1 {
		if _, err := tx.Exec(ctx, `
			UPDATE commerce.loyalty_accounts
			SET points = GREATEST(0, points - $2), updated_at = now()
			WHERE rider_sub = $1`, riderSub, points); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
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

// CreateOffer handles POST /v1/marketplace/offers (Keycloak JWT, operator).
func (h *Handler) CreateOffer(w http.ResponseWriter, r *http.Request) {
	var req createOfferRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil || req.Title == "" || req.CostPoints <= 0 {
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

// Redemption is a recorded offer redemption (commerce.loyalty_redemptions).
type Redemption struct {
	ID             string    `json:"id"`
	RiderSub       string    `json:"rider_sub"`
	OfferID        string    `json:"offer_id"`
	PointsSpent    int       `json:"points_spent"`
	IdempotencyKey string    `json:"idempotency_key"`
	Status         string    `json:"status"`
	CreatedAt      time.Time `json:"created_at"`
}

const redemptionCols = `id, rider_sub, offer_id, points_spent, idempotency_key, status, created_at`

// RedeemOffer handles POST /v1/loyalty/redeem (Keycloak JWT). One SQL
// transaction: replay check on the idempotency key → offer locked FOR UPDATE
// → balance check + decrement (conditional UPDATE, double-spend-safe) →
// redemption record + loyalty-ledger audit entry. Retry-safety: the
// idempotency key (Idempotency-Key header, falling back to offer_id+subject)
// is UNIQUE on commerce.loyalty_redemptions, so a retried request returns
// the original redemption without deducting twice.
func (h *Handler) RedeemOffer(w http.ResponseWriter, r *http.Request) {
	sub := auth.Subject(r.Context())
	if sub == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "authenticated subject required"})
		return
	}
	var req redeemRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil || req.OfferID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "offer_id is required"})
		return
	}
	idemKey := r.Header.Get("Idempotency-Key")
	if idemKey == "" {
		idemKey = req.OfferID + ":" + sub
	}

	tx, err := h.db.Begin(r.Context())
	if err != nil {
		h.internal(w, "begin redeem tx", err)
		return
	}
	defer func() { _ = tx.Rollback(r.Context()) }()

	// Idempotent replay: a redemption already recorded under this key is
	// returned as-is — no second deduction.
	var existing Redemption
	err = tx.QueryRow(r.Context(),
		`SELECT `+redemptionCols+` FROM commerce.loyalty_redemptions WHERE idempotency_key = $1`,
		idemKey).
		Scan(&existing.ID, &existing.RiderSub, &existing.OfferID, &existing.PointsSpent,
			&existing.IdempotencyKey, &existing.Status, &existing.CreatedAt)
	switch {
	case err == nil:
		_ = tx.Commit(r.Context())
		if existing.RiderSub != sub || existing.OfferID != req.OfferID {
			// Same key, different parameters: a real conflict, not a retry.
			writeJSON(w, http.StatusConflict, map[string]string{"error": "idempotency key already used with different redemption parameters"})
			return
		}
		writeJSON(w, http.StatusOK, redemptionResponse(existing, -1))
		return
	case !errors.Is(err, pgx.ErrNoRows):
		h.internal(w, "check redemption idempotency", err)
		return
	}

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
		INSERT INTO commerce.loyalty_accounts (rider_sub) VALUES ($1)
		ON CONFLICT (rider_sub) DO NOTHING`, sub); err != nil {
		h.internal(w, "ensure loyalty account", err)
		return
	}

	var balance int
	if err := tx.QueryRow(r.Context(), `
		UPDATE commerce.loyalty_accounts SET points = points - $2, updated_at = now()
		WHERE rider_sub = $1 AND points >= $2
		RETURNING points`, sub, cost).Scan(&balance); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSON(w, http.StatusPaymentRequired, map[string]string{"error": "insufficient loyalty points"})
			return
		}
		h.internal(w, "deduct points", err)
		return
	}

	red := Redemption{
		ID:             uuid.NewString(),
		RiderSub:       sub,
		OfferID:        req.OfferID,
		PointsSpent:    cost,
		IdempotencyKey: idemKey,
		Status:         "completed",
		CreatedAt:      time.Now().UTC(),
	}
	if _, err := tx.Exec(r.Context(), `
		INSERT INTO commerce.loyalty_redemptions (id, rider_sub, offer_id, points_spent, idempotency_key)
		VALUES ($1, $2, $3, $4, $5)`, red.ID, red.RiderSub, red.OfferID, red.PointsSpent, red.IdempotencyKey); err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			// Concurrent retry with the same key won the race; roll back our
			// deduction and return the recorded redemption instead.
			_ = tx.Rollback(r.Context())
			h.replayRedemption(w, r, idemKey)
			return
		}
		h.internal(w, "record redemption", err)
		return
	}

	// Ledger audit entry (negative delta), idempotent per redemption key.
	if _, err := tx.Exec(r.Context(), `
		INSERT INTO commerce.loyalty_ledger (id, rider_sub, delta, reason, ref_id)
		VALUES ($1, $2, $3, 'offer_redeem', $4)
		ON CONFLICT (ref_id) DO NOTHING`,
		uuid.NewString(), sub, -cost, "redeem:"+idemKey); err != nil {
		h.internal(w, "record redemption ledger entry", err)
		return
	}

	if err := tx.Commit(r.Context()); err != nil {
		h.internal(w, "commit redeem", err)
		return
	}
	writeJSON(w, http.StatusOK, redemptionResponse(red, balance))
}

// replayRedemption returns the already-recorded redemption for an idempotency
// key after a concurrent-insert race.
func (h *Handler) replayRedemption(w http.ResponseWriter, r *http.Request, idemKey string) {
	var red Redemption
	err := h.db.QueryRow(r.Context(),
		`SELECT `+redemptionCols+` FROM commerce.loyalty_redemptions WHERE idempotency_key = $1`,
		idemKey).
		Scan(&red.ID, &red.RiderSub, &red.OfferID, &red.PointsSpent,
			&red.IdempotencyKey, &red.Status, &red.CreatedAt)
	if err != nil {
		h.internal(w, "reload redemption", err)
		return
	}
	writeJSON(w, http.StatusOK, redemptionResponse(red, -1))
}

// redemptionResponse renders a redemption. remaining_points is omitted when
// unknown (-1, i.e. idempotent replays where no deduction happened here).
func redemptionResponse(red Redemption, remaining int) map[string]any {
	resp := map[string]any{
		"redemption_id":     red.ID,
		"redeemed_offer_id": red.OfferID,
		"points_spent":      red.PointsSpent,
		"idempotency_key":   red.IdempotencyKey,
		"status":            red.Status,
	}
	if remaining >= 0 {
		resp["remaining_points"] = remaining
	}
	return resp
}
