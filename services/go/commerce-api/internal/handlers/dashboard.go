package handlers

import (
	"net/http"
)

// GovKPIs is the city KPI rollup (gov-dashboard module, SPEC §1: cost,
// emissions, ridership, uptime).
type GovKPIs struct {
	Revenue30dMinor      int64   `json:"revenue_30d_minor"`
	SettledPayments30d   int64   `json:"settled_payments_30d"`
	RidershipEstimate30d int64   `json:"ridership_estimate_30d"`
	KgCO2AvoidedTotal    float64 `json:"kg_co2_avoided_total"`
	CarbonCreditsTotal   float64 `json:"carbon_credits_total"`
	VehiclesTotal        int64   `json:"vehicles_total"`
	VehiclesActive       int64   `json:"vehicles_active"`
	FleetUptimePct       float64 `json:"fleet_uptime_pct"`
	StationsAvailableKg  float64 `json:"stations_available_kg"`
	OpenIncidents        int64   `json:"open_incidents"`
}

// GetGovKPIs handles GET /v1/gov/kpis — SQL rollups across all domain schemas.
func (h *Handler) GetGovKPIs(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var k GovKPIs

	if err := h.db.QueryRow(ctx, `
		SELECT COALESCE(sum(amount_minor),0), count(*)
		FROM commerce.fare_payments
		WHERE status = 'settled' AND created_at > now() - interval '30 days'`).
		Scan(&k.Revenue30dMinor, &k.SettledPayments30d); err != nil {
		h.internal(w, "kpi: payments rollup", err)
		return
	}
	// Ridership estimate: one settled fare ≈ one ride.
	k.RidershipEstimate30d = k.SettledPayments30d

	if err := h.db.QueryRow(ctx, `
		SELECT COALESCE(sum(kg_co2_avoided),0), COALESCE(sum(credits),0)
		FROM citizen.carbon_credits`).Scan(&k.KgCO2AvoidedTotal, &k.CarbonCreditsTotal); err != nil {
		h.internal(w, "kpi: carbon rollup", err)
		return
	}

	if err := h.db.QueryRow(ctx, `
		SELECT count(*), count(*) FILTER (WHERE status = 'active')
		FROM fleet.vehicles`).Scan(&k.VehiclesTotal, &k.VehiclesActive); err != nil {
		h.internal(w, "kpi: fleet rollup", err)
		return
	}
	if k.VehiclesTotal > 0 {
		k.FleetUptimePct = float64(k.VehiclesActive) / float64(k.VehiclesTotal) * 100
	}

	if err := h.db.QueryRow(ctx, `
		SELECT COALESCE(sum(available_kg),0) FROM infra.stations`).
		Scan(&k.StationsAvailableKg); err != nil {
		h.internal(w, "kpi: stations rollup", err)
		return
	}

	if err := h.db.QueryRow(ctx, `
		SELECT count(*) FROM infra.incidents WHERE status IN ('open','acknowledged')`).
		Scan(&k.OpenIncidents); err != nil {
		h.internal(w, "kpi: incidents rollup", err)
		return
	}

	writeJSON(w, http.StatusOK, k)
}
