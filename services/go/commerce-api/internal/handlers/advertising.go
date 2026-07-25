package handlers

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
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

// maxCampaignNameLen bounds campaign names (audit: no validation).
const maxCampaignNameLen = 200

// CreateCampaign handles POST /v1/ads/campaigns (Keycloak JWT). Validation
// (audit defects): name required (trimmed, ≤200 chars), budget must not be
// negative, and ends_at must not precede starts_at.
func (h *Handler) CreateCampaign(w http.ResponseWriter, r *http.Request) {
	var req createCampaignRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" || len(req.Name) > maxCampaignNameLen {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "name is required (1-200 chars)"})
		return
	}
	if req.BudgetMinor < 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "budget_minor must not be negative"})
		return
	}
	if req.StartsAt != nil && req.EndsAt != nil && req.EndsAt.Before(*req.StartsAt) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "ends_at must not be before starts_at"})
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

// campaignStatusTransitions is the campaign lifecycle (audit: any → any was
// accepted, so ended campaigns could be resurrected). ended is terminal;
// staying in the same status is an idempotent no-op.
var campaignStatusTransitions = map[string]map[string]bool{
	"draft":  {"draft": true, "active": true, "ended": true},
	"active": {"active": true, "paused": true, "ended": true},
	"paused": {"paused": true, "active": true, "ended": true},
	"ended":  {"ended": true},
}

type updateCampaignRequest struct {
	Status string `json:"status"`
}

// UpdateCampaign handles PATCH /v1/ads/campaigns/{id} (Keycloak JWT).
// Enforces the lifecycle above: an illegal transition (e.g. ended → active)
// is rejected with 409.
func (h *Handler) UpdateCampaign(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var req updateCampaignRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil || !validCampaignStatuses[req.Status] {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "status must be one of draft|active|paused|ended"})
		return
	}
	var current string
	err := h.db.QueryRow(r.Context(),
		`SELECT status FROM commerce.ad_campaigns WHERE id = $1`, id).Scan(&current)
	if errors.Is(err, pgx.ErrNoRows) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "campaign not found"})
		return
	}
	if err != nil {
		h.internal(w, "load campaign status", err)
		return
	}
	if !campaignStatusTransitions[current][req.Status] {
		writeJSON(w, http.StatusConflict, map[string]string{
			"error": "invalid status transition from " + current + " to " + req.Status,
		})
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
