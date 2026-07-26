package handlers

import (
	"net/http"

	"go.uber.org/zap"
)

// GovKPIs is the city KPI rollup (gov-dashboard module, SPEC §1: cost,
// emissions, ridership, uptime). Honesty contract (business-logic audit):
// every section degrades independently — a failed rollup leaves its fields
// null and names the source in degraded (never a fabricated plausible
// value). fleet_uptime_pct is null until a time-based availability source
// (telemetry/incident rollup) exists; the static status mix is reported
// separately as fleet_active_ratio_pct.
type GovKPIs struct {
	Revenue30dMinor      *int64   `json:"revenue_30d_minor"`
	SettledPayments30d   *int64   `json:"settled_payments_30d"`
	RidershipEstimate30d *int64   `json:"ridership_estimate_30d"` // one settled fare ≈ one ride
	KgCO2AvoidedTotal    *float64 `json:"kg_co2_avoided_total"`
	CarbonCreditsTotal   *float64 `json:"carbon_credits_total"`
	VehiclesTotal        *int64   `json:"vehicles_total"`
	VehiclesActive       *int64   `json:"vehicles_active"`
	FleetActiveRatioPct  *float64 `json:"fleet_active_ratio_pct"`
	FleetUptimePct       *float64 `json:"fleet_uptime_pct"`
	FleetUptimeNote      string   `json:"fleet_uptime_note"`
	StationsAvailableKg  *float64 `json:"stations_available_kg"`
	OpenIncidents        *int64   `json:"open_incidents"` // open + acknowledged + in_progress
	Partial              bool     `json:"partial"`
	Degraded             []string `json:"degraded,omitempty"`
}

const fleetUptimeNote = "share of the fleet with telemetry in the last 15 minutes (time-based availability); fleet_active_ratio_pct is the static status mix, reported separately"

// GetGovKPIs handles GET /v1/gov/kpis — SQL rollups across all domain
// schemas. Each rollup is independent: failures degrade the response
// partially (fields null + source named in degraded) instead of failing the
// whole KPI snapshot with 500.
func (h *Handler) GetGovKPIs(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var k GovKPIs
	k.FleetUptimeNote = fleetUptimeNote
	degraded := func(source string, err error) {
		h.log.Error("kpi rollup degraded", zap.String("source", source), zap.Error(err))
		k.Degraded = append(k.Degraded, source)
	}

	// fare-payments rollup (30d settled).
	{
		var revenue, count int64
		if err := h.db.QueryRow(ctx, `
			SELECT COALESCE(sum(amount_minor),0), count(*)
			FROM commerce.fare_payments
			WHERE status = 'settled' AND created_at > now() - interval '30 days'`).
			Scan(&revenue, &count); err != nil {
			degraded("fare-payments", err)
		} else {
			k.Revenue30dMinor = &revenue
			k.SettledPayments30d = &count
			ridership := count // one settled fare ≈ one ride
			k.RidershipEstimate30d = &ridership
		}
	}

	// carbon rollup.
	{
		var kg, credits float64
		if err := h.db.QueryRow(ctx, `
			SELECT COALESCE(sum(kg_co2_avoided),0), COALESCE(sum(credits),0)
			FROM citizen.carbon_credits`).Scan(&kg, &credits); err != nil {
			degraded("carbon", err)
		} else {
			k.KgCO2AvoidedTotal = &kg
			k.CarbonCreditsTotal = &credits
		}
	}

	// fleet rollup: static status mix (active/total) AND time-based
	// availability — fleet_uptime_pct is the share of the fleet that
	// actually reported telemetry in the last 15 minutes (a bus broken for
	// weeks but still status 'active' stops counting).
	{
		var total, active int64
		if err := h.db.QueryRow(ctx, `
			SELECT count(*), count(*) FILTER (WHERE status = 'active')
			FROM fleet.vehicles`).Scan(&total, &active); err != nil {
			degraded("fleet", err)
		} else {
			k.VehiclesTotal = &total
			k.VehiclesActive = &active
			if total > 0 {
				ratio := float64(active) / float64(total) * 100
				k.FleetActiveRatioPct = &ratio
			}
			var reporting int64
			if err := h.db.QueryRow(ctx, `
				SELECT count(DISTINCT bus_id) FROM fleet.telemetry
				WHERE ts > now() - interval '15 minutes'`).Scan(&reporting); err != nil {
				degraded("fleet-telemetry", err)
			} else if total > 0 {
				uptime := float64(reporting) / float64(total) * 100
				k.FleetUptimePct = &uptime
			}
		}
	}

	// station inventory rollup.
	{
		var kg float64
		if err := h.db.QueryRow(ctx, `
			SELECT COALESCE(sum(available_kg),0) FROM infra.stations`).
			Scan(&kg); err != nil {
			degraded("stations", err)
		} else {
			k.StationsAvailableKg = &kg
		}
	}

	// open-incident rollup. Includes in_progress: incidents being worked
	// must not vanish from the KPI (audit defect).
	{
		var open int64
		if err := h.db.QueryRow(ctx, `
			SELECT count(*) FROM infra.incidents WHERE status IN ('open','acknowledged','in_progress')`).
			Scan(&open); err != nil {
			degraded("incidents", err)
		} else {
			k.OpenIncidents = &open
		}
	}

	k.Partial = len(k.Degraded) > 0
	writeJSON(w, http.StatusOK, k)
}
