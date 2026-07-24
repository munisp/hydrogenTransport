package handlers

import (
	"net/http"
	"time"
)

// CarbonCredit mirrors citizen.carbon_credits (carbon-credits module).
type CarbonCredit struct {
	ID           string    `json:"id"`
	Period       string    `json:"period"`
	KgCO2Avoided float64   `json:"kg_co2_avoided"`
	Credits      float64   `json:"credits"`
	IssuedAt     time.Time `json:"issued_at"`
}

// ListCarbonCredits handles GET /v1/carbon/credits?period=.
func (h *Handler) ListCarbonCredits(w http.ResponseWriter, r *http.Request) {
	if !h.requireDB(w) {
		return
	}
	query := `SELECT id, period, COALESCE(kg_co2_avoided,0), COALESCE(credits,0), issued_at
		FROM citizen.carbon_credits`
	args := []any{}
	if period := r.URL.Query().Get("period"); period != "" {
		query += ` WHERE period = $1`
		args = append(args, period)
	}
	query += ` ORDER BY issued_at DESC LIMIT 200`

	rows, err := h.db.Query(r.Context(), query, args...)
	if err != nil {
		h.internal(w, "list carbon credits", err)
		return
	}
	defer rows.Close()

	credits := []CarbonCredit{}
	for rows.Next() {
		var c CarbonCredit
		if err := rows.Scan(&c.ID, &c.Period, &c.KgCO2Avoided, &c.Credits, &c.IssuedAt); err != nil {
			h.internal(w, "scan carbon credit", err)
			return
		}
		credits = append(credits, c)
	}
	if err := rows.Err(); err != nil {
		h.internal(w, "iterate carbon credits", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"credits": credits})
}

// CarbonSummary handles GET /v1/carbon/credits/summary — fleet-wide CO2
// avoidance and issued-credit totals.
func (h *Handler) CarbonSummary(w http.ResponseWriter, r *http.Request) {
	if !h.requireDB(w) {
		return
	}
	var kg float64
	var credits float64
	var periods int
	if err := h.db.QueryRow(r.Context(), `
		SELECT COALESCE(sum(kg_co2_avoided),0), COALESCE(sum(credits),0), count(DISTINCT period)
		FROM citizen.carbon_credits`).Scan(&kg, &credits, &periods); err != nil {
		h.internal(w, "carbon summary", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"kg_co2_avoided_total": kg,
		"credits_total":        credits,
		"periods":              periods,
	})
}
