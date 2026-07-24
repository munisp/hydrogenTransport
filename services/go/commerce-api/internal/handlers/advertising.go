package handlers

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
)

// Campaign mirrors commerce.ad_campaigns (advertising module).
type Campaign struct {
	ID          string     `json:"id"`
	Name        string     `json:"name"`
	Advertiser  string     `json:"advertiser"`
	BudgetMinor int64      `json:"budget_minor"`
	Status      string     `json:"status"` // draft|active|paused|ended
	StartsAt    *time.Time `json:"starts_at,omitempty"`
	EndsAt      *time.Time `json:"ends_at,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
}

const campaignCols = `id, name, advertiser, budget_minor, status, starts_at, ends_at, created_at`

func scanCampaign(row pgx.Row) (Campaign, error) {
	var c Campaign
	err := row.Scan(&c.ID, &c.Name, &c.Advertiser, &c.BudgetMinor, &c.Status, &c.StartsAt, &c.EndsAt, &c.CreatedAt)
	return c, err
}

// ListCampaigns handles GET /v1/ads/campaigns?status=.
func (h *Handler) ListCampaigns(w http.ResponseWriter, r *http.Request) {
	query := `SELECT ` + campaignCols + ` FROM commerce.ad_campaigns`
	args := []any{}
	if status := r.URL.Query().Get("status"); status != "" {
		query += ` WHERE status = $1`
		args = append(args, status)
	}
	query += ` ORDER BY created_at DESC LIMIT 200`

	rows, err := h.db.Query(r.Context(), query, args...)
	if err != nil {
		h.internal(w, "list campaigns", err)
		return
	}
	defer rows.Close()

	campaigns := []Campaign{}
	for rows.Next() {
		c, err := scanCampaign(rows)
		if err != nil {
			h.internal(w, "scan campaign", err)
			return
		}
		campaigns = append(campaigns, c)
	}
	if err := rows.Err(); err != nil {
		h.internal(w, "iterate campaigns", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"campaigns": campaigns})
}

// GetCampaign handles GET /v1/ads/campaigns/{id}.
func (h *Handler) GetCampaign(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	c, err := scanCampaign(h.db.QueryRow(r.Context(),
		`SELECT `+campaignCols+` FROM commerce.ad_campaigns WHERE id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "campaign not found"})
		return
	}
	if err != nil {
		h.internal(w, "get campaign", err)
		return
	}
	writeJSON(w, http.StatusOK, c)
}

type createCampaignRequest struct {
	Name        string     `json:"name"`
	Advertiser  string     `json:"advertiser"`
	BudgetMinor int64      `json:"budget_minor"`
	StartsAt    *time.Time `json:"starts_at"`
	EndsAt      *time.Time `json:"ends_at"`
}

// CreateCampaign handles POST /v1/ads/campaigns (Keycloak JWT).
func (h *Handler) CreateCampaign(w http.ResponseWriter, r *http.Request) {
	var req createCampaignRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Name == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "name is required"})
		return
	}
	c, err := scanCampaign(h.db.QueryRow(r.Context(), `
		INSERT INTO commerce.ad_campaigns (name, advertiser, budget_minor, starts_at, ends_at)
		VALUES ($1, $2, $3, $4, $5) RETURNING `+campaignCols,
		req.Name, req.Advertiser, req.BudgetMinor, req.StartsAt, req.EndsAt))
	if err != nil {
		h.internal(w, "create campaign", err)
		return
	}
	writeJSON(w, http.StatusCreated, c)
}

var validCampaignStatuses = map[string]bool{
	"draft": true, "active": true, "paused": true, "ended": true,
}

type updateCampaignRequest struct {
	Status string `json:"status"`
}

// UpdateCampaign handles PATCH /v1/ads/campaigns/{id} (Keycloak JWT).
func (h *Handler) UpdateCampaign(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var req updateCampaignRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || !validCampaignStatuses[req.Status] {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "status must be one of draft|active|paused|ended"})
		return
	}
	c, err := scanCampaign(h.db.QueryRow(r.Context(), `
		UPDATE commerce.ad_campaigns SET status = $2 WHERE id = $1
		RETURNING `+campaignCols, id, req.Status))
	if errors.Is(err, pgx.ErrNoRows) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "campaign not found"})
		return
	}
	if err != nil {
		h.internal(w, "update campaign", err)
		return
	}
	writeJSON(w, http.StatusOK, c)
}
