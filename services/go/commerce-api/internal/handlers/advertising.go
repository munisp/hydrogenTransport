package handlers

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Campaign mirrors commerce.ad_campaigns (advertising module).
// CommittedMinor / RemainingBudgetMinor track spend against the budget:
// committed = sum of placement costs (commerce.ad_placements.cost_minor).
type Campaign struct {
	ID                   string     `json:"id"`
	Name                 string     `json:"name"`
	Advertiser           string     `json:"advertiser"`
	BudgetMinor          int64      `json:"budget_minor"`
	CommittedMinor       int64      `json:"committed_minor"`
	RemainingBudgetMinor int64      `json:"remaining_budget_minor"`
	Status               string     `json:"status"` // draft|active|paused|ended
	StartsAt             *time.Time `json:"starts_at,omitempty"`
	EndsAt               *time.Time `json:"ends_at,omitempty"`
	CreatedAt            time.Time  `json:"created_at"`
}

const campaignCols = `id, name, advertiser, budget_minor,
	(SELECT COALESCE(sum(p.cost_minor),0) FROM commerce.ad_placements p WHERE p.campaign_id = ad_campaigns.id),
	status, starts_at, ends_at, created_at`

func scanCampaign(row pgx.Row) (Campaign, error) {
	var c Campaign
	err := row.Scan(&c.ID, &c.Name, &c.Advertiser, &c.BudgetMinor, &c.CommittedMinor,
		&c.Status, &c.StartsAt, &c.EndsAt, &c.CreatedAt)
	c.RemainingBudgetMinor = c.BudgetMinor - c.CommittedMinor
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

// ---------------------------------------------------------------------------
// Ad inventory & placements (BUSINESS_LOGIC_AUDIT §20: SPEC "on-bus/digital
// ad inventory & campaigns" was inexpressible — no inventory entity existed).
// Inventory = a sellable slot (bus-side wrap, interior screen, shelter,
// digital screen). A placement books a campaign onto an inventory slot for a
// time window at a cost; the exclusion constraint in migration 0005 makes
// overlapping placements on the same slot impossible at the DB level, and
// the budget check below makes overspending the campaign budget impossible
// at the API level.
// ---------------------------------------------------------------------------

// AdInventory mirrors commerce.ad_inventory.
type AdInventory struct {
	ID        string    `json:"id"`
	Kind      string    `json:"kind"`
	BusID     *string   `json:"bus_id,omitempty"`
	Label     string    `json:"label"`
	Active    bool      `json:"active"`
	CreatedAt time.Time `json:"created_at"`
}

const adInventoryCols = `id, kind, bus_id, label, active, created_at`

// validInventoryKinds enumerates the sellable slot kinds.
var validInventoryKinds = map[string]bool{
	"bus-side": true, "bus-interior": true, "shelter": true, "digital-screen": true,
}

func scanAdInventory(row pgx.Row) (AdInventory, error) {
	var i AdInventory
	err := row.Scan(&i.ID, &i.Kind, &i.BusID, &i.Label, &i.Active, &i.CreatedAt)
	return i, err
}

// ListAdInventory handles GET /v1/ads/inventory?kind=&active=.
func (h *Handler) ListAdInventory(w http.ResponseWriter, r *http.Request) {
	query := `SELECT ` + adInventoryCols + ` FROM commerce.ad_inventory`
	args := []any{}
	conds := ""
	if kind := r.URL.Query().Get("kind"); kind != "" {
		args = append(args, kind)
		conds += fmt.Sprintf(" AND kind = $%d", len(args))
	}
	if active := r.URL.Query().Get("active"); active != "" {
		args = append(args, active == "true")
		conds += fmt.Sprintf(" AND active = $%d", len(args))
	}
	if conds != "" {
		query += ` WHERE ` + conds[len(" AND "):]
	}
	query += ` ORDER BY created_at DESC LIMIT 200`

	rows, err := h.db.Query(r.Context(), query, args...)
	if err != nil {
		h.internal(w, "list ad inventory", err)
		return
	}
	defer rows.Close()

	inventory := []AdInventory{}
	for rows.Next() {
		i, err := scanAdInventory(rows)
		if err != nil {
			h.internal(w, "scan ad inventory", err)
			return
		}
		inventory = append(inventory, i)
	}
	if err := rows.Err(); err != nil {
		h.internal(w, "iterate ad inventory", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"inventory": inventory})
}

type createInventoryRequest struct {
	Kind  string  `json:"kind"`
	BusID *string `json:"bus_id"`
	Label string  `json:"label"`
}

// CreateAdInventory handles POST /v1/ads/inventory (Keycloak JWT, operator).
// bus-side / bus-interior slots must reference a real vehicle (FK → 422).
func (h *Handler) CreateAdInventory(w http.ResponseWriter, r *http.Request) {
	var req createInventoryRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
		return
	}
	if !validInventoryKinds[req.Kind] {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "kind must be one of bus-side|bus-interior|shelter|digital-screen"})
		return
	}
	req.Label = strings.TrimSpace(req.Label)
	if req.Label == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "label is required"})
		return
	}
	if (req.Kind == "bus-side" || req.Kind == "bus-interior") && req.BusID == nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bus_id is required for on-bus inventory"})
		return
	}
	i, err := scanAdInventory(h.db.QueryRow(r.Context(), `
		INSERT INTO commerce.ad_inventory (kind, bus_id, label)
		VALUES ($1, $2, $3) RETURNING `+adInventoryCols,
		req.Kind, req.BusID, req.Label))
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23503" {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": "bus_id does not reference a known vehicle"})
		return
	}
	if err != nil {
		h.internal(w, "create ad inventory", err)
		return
	}
	writeJSON(w, http.StatusCreated, i)
}

// AdPlacement mirrors commerce.ad_placements.
type AdPlacement struct {
	ID          string    `json:"id"`
	CampaignID  string    `json:"campaign_id"`
	InventoryID string    `json:"inventory_id"`
	StartsAt    time.Time `json:"starts_at"`
	EndsAt      time.Time `json:"ends_at"`
	CostMinor   int64     `json:"cost_minor"`
	CreatedAt   time.Time `json:"created_at"`
}

const adPlacementCols = `id, campaign_id, inventory_id, starts_at, ends_at, cost_minor, created_at`

func scanAdPlacement(row pgx.Row) (AdPlacement, error) {
	var p AdPlacement
	err := row.Scan(&p.ID, &p.CampaignID, &p.InventoryID, &p.StartsAt, &p.EndsAt, &p.CostMinor, &p.CreatedAt)
	return p, err
}

// ListAdPlacements handles GET /v1/ads/placements?campaign_id=&inventory_id=.
func (h *Handler) ListAdPlacements(w http.ResponseWriter, r *http.Request) {
	query := `SELECT ` + adPlacementCols + ` FROM commerce.ad_placements`
	args := []any{}
	conds := ""
	if cid := r.URL.Query().Get("campaign_id"); cid != "" {
		args = append(args, cid)
		conds += fmt.Sprintf(" AND campaign_id = $%d", len(args))
	}
	if iid := r.URL.Query().Get("inventory_id"); iid != "" {
		args = append(args, iid)
		conds += fmt.Sprintf(" AND inventory_id = $%d", len(args))
	}
	if conds != "" {
		query += ` WHERE ` + conds[len(" AND "):]
	}
	query += ` ORDER BY starts_at DESC LIMIT 200`

	rows, err := h.db.Query(r.Context(), query, args...)
	if err != nil {
		h.internal(w, "list ad placements", err)
		return
	}
	defer rows.Close()

	placements := []AdPlacement{}
	for rows.Next() {
		p, err := scanAdPlacement(rows)
		if err != nil {
			h.internal(w, "scan ad placement", err)
			return
		}
		placements = append(placements, p)
	}
	if err := rows.Err(); err != nil {
		h.internal(w, "iterate ad placements", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"placements": placements})
}

type createPlacementRequest struct {
	CampaignID  string    `json:"campaign_id"`
	InventoryID string    `json:"inventory_id"`
	StartsAt    time.Time `json:"starts_at"`
	EndsAt      time.Time `json:"ends_at"`
	CostMinor   int64     `json:"cost_minor"`
}

// CreateAdPlacement handles POST /v1/ads/placements (Keycloak JWT,
// operator). Rules, all enforced before/within the insert transaction:
//   - ends_at must be after starts_at; cost_minor must not be negative;
//   - the campaign must exist and be bookable (draft|active|paused — never
//     ended) and the inventory slot must exist and be active;
//   - budget tracking: committed placement cost + this cost must not exceed
//     the campaign budget (409 budget_exceeded);
//   - date overlap: the 0005 exclusion constraint rejects double-booking a
//     slot (409 slot_already_booked).
func (h *Handler) CreateAdPlacement(w http.ResponseWriter, r *http.Request) {
	var req createPlacementRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
		return
	}
	if req.CampaignID == "" || req.InventoryID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "campaign_id and inventory_id are required"})
		return
	}
	if !req.EndsAt.After(req.StartsAt) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "ends_at must be after starts_at"})
		return
	}
	if req.CostMinor < 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "cost_minor must not be negative"})
		return
	}

	tx, err := h.db.Begin(r.Context())
	if err != nil {
		h.internal(w, "begin placement tx", err)
		return
	}
	defer func() { _ = tx.Rollback(r.Context()) }()

	var status string
	var budget int64
	err = tx.QueryRow(r.Context(), `
		SELECT status, COALESCE(budget_minor,0) FROM commerce.ad_campaigns WHERE id = $1 FOR UPDATE`,
		req.CampaignID).Scan(&status, &budget)
	if errors.Is(err, pgx.ErrNoRows) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "campaign not found"})
		return
	}
	if err != nil {
		h.internal(w, "load campaign for placement", err)
		return
	}
	if status == "ended" {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "cannot book placements on an ended campaign"})
		return
	}

	var invActive bool
	err = tx.QueryRow(r.Context(),
		`SELECT active FROM commerce.ad_inventory WHERE id = $1`, req.InventoryID).Scan(&invActive)
	if errors.Is(err, pgx.ErrNoRows) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "inventory slot not found"})
		return
	}
	if err != nil {
		h.internal(w, "load inventory for placement", err)
		return
	}
	if !invActive {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "inventory slot is inactive"})
		return
	}

	var committed int64
	if err := tx.QueryRow(r.Context(), `
		SELECT COALESCE(sum(cost_minor),0) FROM commerce.ad_placements WHERE campaign_id = $1`,
		req.CampaignID).Scan(&committed); err != nil {
		h.internal(w, "compute committed spend", err)
		return
	}
	if committed+req.CostMinor > budget {
		writeJSON(w, http.StatusConflict, map[string]any{
			"error":           "budget_exceeded",
			"budget_minor":    budget,
			"committed_minor": committed,
			"requested_minor": req.CostMinor,
		})
		return
	}

	p, err := scanAdPlacement(tx.QueryRow(r.Context(), `
		INSERT INTO commerce.ad_placements (campaign_id, inventory_id, starts_at, ends_at, cost_minor)
		VALUES ($1, $2, $3, $4, $5) RETURNING `+adPlacementCols,
		req.CampaignID, req.InventoryID, req.StartsAt, req.EndsAt, req.CostMinor))
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23P01" { // exclusion_violation
			writeJSON(w, http.StatusConflict, map[string]string{"error": "slot_already_booked: an overlapping placement exists for this inventory slot"})
			return
		}
		h.internal(w, "create ad placement", err)
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		h.internal(w, "commit ad placement", err)
		return
	}
	writeJSON(w, http.StatusCreated, p)
}
