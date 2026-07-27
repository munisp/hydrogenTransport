package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
)

// ComplianceReport is a generated regulatory/safety compliance report
// (compliance-reporting module).
type ComplianceReport struct {
	ID          string          `json:"id"`
	GeneratedAt time.Time       `json:"generated_at"`
	Report      json.RawMessage `json:"report"`
}

// defaultReportDays / maxReportDays bound the reporting window
// (BUSINESS_LOGIC_AUDIT §9: the window was hardcoded to 30 days).
const (
	defaultReportDays = 30
	maxReportDays     = 365
)

// compliancePack is one energy-domain template for the report sections that
// differ per fleet energy vector (Wave 5). Sections that are identical for
// every fleet (incident status/severity, MTTR, maintenance backlog, fleet
// availability, station inventory) stay shared in buildReport.
type compliancePack struct {
	// LeakAging enables the unresolved-leak aging section over LeakTypes
	// (H2 fleet: 'h2_leak'; CNG fleet: 'cng_leak').
	LeakAging bool
	LeakTypes []string
	// BatteryThermal adds the battery-thermal incident section (open count +
	// aging over 'battery_thermal' incidents) in place of leak aging.
	BatteryThermal bool
}

// compliancePacks is the Wave-5 per-domain template map. 'h2' reproduces the
// pre-Wave-5 report exactly (default — 100% backward compatible); battery
// drops the H2 leak aging and adds battery-thermal incident categories;
// diesel drops the leak sections entirely; cng keeps gas-leak aging over its
// own incident type.
var compliancePacks = map[string]compliancePack{
	"h2":      {LeakAging: true, LeakTypes: []string{"h2_leak"}},
	"battery": {BatteryThermal: true},
	"diesel":  {},
	"cng":     {LeakAging: true, LeakTypes: []string{"cng_leak"}},
}

// defaultComplianceDomain resolves the report domain when the caller does not
// pass ?domain= : the fleet-config env COMPLIANCE_DOMAIN if valid, else 'h2'.
func defaultComplianceDomain() string {
	if d := os.Getenv("COMPLIANCE_DOMAIN"); d != "" {
		if _, ok := compliancePacks[d]; ok {
			return d
		}
	}
	return "h2"
}

// ListComplianceReports handles GET /v1/compliance/reports.
func (h *Handler) ListComplianceReports(w http.ResponseWriter, r *http.Request) {
	rows, err := h.db.Query(r.Context(), `
		SELECT id, generated_at, report FROM infra.compliance_reports
		ORDER BY generated_at DESC LIMIT 100`)
	if err != nil {
		h.internal(w, "list compliance reports", err)
		return
	}
	defer rows.Close()

	reports := []ComplianceReport{}
	for rows.Next() {
		var rep ComplianceReport
		if err := rows.Scan(&rep.ID, &rep.GeneratedAt, &rep.Report); err != nil {
			h.internal(w, "scan compliance report", err)
			return
		}
		reports = append(reports, rep)
	}
	if err := rows.Err(); err != nil {
		h.internal(w, "iterate compliance reports", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"reports": reports})
}

// GetComplianceReport handles GET /v1/compliance/reports/{id}.
func (h *Handler) GetComplianceReport(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var rep ComplianceReport
	err := h.db.QueryRow(r.Context(), `
		SELECT id, generated_at, report FROM infra.compliance_reports WHERE id = $1`, id).
		Scan(&rep.ID, &rep.GeneratedAt, &rep.Report)
	if errors.Is(err, pgx.ErrNoRows) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "report not found"})
		return
	}
	if err != nil {
		h.internal(w, "get compliance report", err)
		return
	}
	writeJSON(w, http.StatusOK, rep)
}

// buildReport aggregates the full compliance picture over the trailing
// `days` window: incident state, severity mix, MTTR, maintenance-prediction
// backlog + open work orders, fleet availability, and station inventory
// (BUSINESS_LOGIC_AUDIT §9: the report was an incidents+stations rollup
// only). Wave 5: domain-specific sections come from the compliancePacks
// template for `domain` (h2 = the pre-Wave-5 report, unchanged). Every
// section degrades independently — a failed rollup names itself in degraded
// instead of failing the whole report.
func (h *Handler) buildReport(ctx context.Context, days int, domain string) map[string]any {
	pack, ok := compliancePacks[domain]
	if !ok {
		pack = compliancePacks["h2"]
		domain = "h2"
	}
	report := map[string]any{
		"generated_at": time.Now().UTC().Format(time.RFC3339),
		"standard":     "H2Fleet safety & compliance (SPEC §3.4 infra schema)",
		"domain":       domain,
		"period_days":  days,
		"period_start": time.Now().AddDate(0, 0, -days).UTC().Format(time.RFC3339),
	}
	degraded := []string{}

	// Incidents by status (current backlog — point-in-time, not windowed).
	{
		incidentStatus := map[string]int{}
		rows, err := h.db.Query(ctx, `SELECT status, count(*) FROM infra.incidents GROUP BY status`)
		if err != nil {
			degraded = append(degraded, "incidents_by_status")
		} else {
			for rows.Next() {
				var status string
				var n int
				if err := rows.Scan(&status, &n); err != nil {
					degraded = append(degraded, "incidents_by_status")
					break
				}
				incidentStatus[status] = n
			}
			rows.Close()
			report["incidents_by_status"] = incidentStatus
		}
	}

	// Incidents by severity over the window.
	{
		incidentSeverity := map[string]int{}
		rows, err := h.db.Query(ctx, `
			SELECT severity, count(*) FROM infra.incidents
			WHERE opened_at > now() - make_interval(days => $1) GROUP BY severity`, days)
		if err != nil {
			degraded = append(degraded, "incidents_by_severity")
		} else {
			for rows.Next() {
				var severity string
				var n int
				if err := rows.Scan(&severity, &n); err != nil {
					degraded = append(degraded, "incidents_by_severity")
					break
				}
				incidentSeverity[severity] = n
			}
			rows.Close()
			report["incidents_by_severity_period"] = incidentSeverity
		}
	}

	// Unresolved-leak aging: how long open leaks have been waiting. Domain
	// pack: h2 → 'h2_leak', cng → 'cng_leak'; battery/diesel packs drop this
	// section (battery gets thermal categories below, diesel has no gas
	// leak domain).
	if pack.LeakAging {
		var open int
		var avgAgeH, oldestH *float64
		if err := h.db.QueryRow(ctx, `
			SELECT count(*),
			       avg(EXTRACT(EPOCH FROM (now() - opened_at)) / 3600)::float8,
			       max(EXTRACT(EPOCH FROM (now() - opened_at)) / 3600)::float8
			FROM infra.incidents
			WHERE type = ANY($1) AND status IN ('open','acknowledged','in_progress')`,
			pack.LeakTypes).
			Scan(&open, &avgAgeH, &oldestH); err != nil {
			degraded = append(degraded, "leak_aging")
		} else {
			report["unresolved_leaks"] = map[string]any{
				"open":             open,
				"avg_age_hours":    avgAgeH,
				"oldest_age_hours": oldestH,
			}
		}
	}

	// Battery-thermal incident categories (battery pack only): open thermal
	// events and their aging, the EV analogue of leak aging.
	if pack.BatteryThermal {
		var open int
		var avgAgeH, oldestH *float64
		if err := h.db.QueryRow(ctx, `
			SELECT count(*),
			       avg(EXTRACT(EPOCH FROM (now() - opened_at)) / 3600)::float8,
			       max(EXTRACT(EPOCH FROM (now() - opened_at)) / 3600)::float8
			FROM infra.incidents
			WHERE type = 'battery_thermal' AND status IN ('open','acknowledged','in_progress')`).
			Scan(&open, &avgAgeH, &oldestH); err != nil {
			degraded = append(degraded, "battery_thermal")
		} else {
			report["battery_thermal_incidents"] = map[string]any{
				"open":             open,
				"avg_age_hours":    avgAgeH,
				"oldest_age_hours": oldestH,
			}
		}
	}

	// MTTR: mean time to resolve incidents closed inside the window.
	{
		var resolved int
		var mttrH *float64
		if err := h.db.QueryRow(ctx, `
			SELECT count(*), avg(EXTRACT(EPOCH FROM (resolved_at - opened_at)) / 3600)::float8
			FROM infra.incidents
			WHERE status = 'resolved' AND resolved_at IS NOT NULL
			  AND opened_at > now() - make_interval(days => $1)`, days).Scan(&resolved, &mttrH); err != nil {
			degraded = append(degraded, "mttr")
		} else {
			report["mttr"] = map[string]any{
				"resolved_in_period": resolved,
				"avg_hours":          mttrH,
			}
		}
	}

	// Maintenance-prediction backlog and depot work-order state.
	{
		var total, highRisk int
		if err := h.db.QueryRow(ctx, `
			SELECT count(*), count(*) FILTER (WHERE risk_score >= 0.7)
			FROM fleet.maintenance_predictions
			WHERE created_at > now() - make_interval(days => $1)`, days).
			Scan(&total, &highRisk); err != nil {
			degraded = append(degraded, "maintenance_predictions")
		} else {
			report["maintenance_predictions_period"] = map[string]any{
				"total":     total,
				"high_risk": highRisk,
			}
		}
		var openWO int
		if err := h.db.QueryRow(ctx,
			`SELECT count(*) FROM infra.work_orders WHERE status <> 'closed'`).Scan(&openWO); err != nil {
			degraded = append(degraded, "work_orders")
		} else {
			report["work_orders_open"] = openWO
		}
	}

	// Fleet availability: share of the fleet reporting telemetry in the last
	// 24h (time-based, not a static status mix).
	{
		var totalV, reporting int64
		if err := h.db.QueryRow(ctx, `
			SELECT count(*) FROM fleet.vehicles`).Scan(&totalV); err != nil {
			degraded = append(degraded, "fleet")
		} else if err := h.db.QueryRow(ctx, `
			SELECT count(DISTINCT bus_id) FROM fleet.telemetry
			WHERE ts > now() - interval '24 hours'`).Scan(&reporting); err != nil {
			degraded = append(degraded, "fleet")
		} else {
			availability := map[string]any{
				"vehicles_total":     totalV,
				"reporting_last_24h": reporting,
				"availability_pct":   nil,
			}
			if totalV > 0 {
				availability["availability_pct"] = float64(reporting) / float64(totalV) * 100
			}
			report["fleet_availability"] = availability
		}
	}

	// Station inventory.
	{
		var stationCount int
		var totalCapacity, totalAvailable float64
		if err := h.db.QueryRow(ctx, `
			SELECT count(*), COALESCE(sum(capacity_kg),0), COALESCE(sum(available_kg),0)
			FROM infra.stations`).Scan(&stationCount, &totalCapacity, &totalAvailable); err != nil {
			degraded = append(degraded, "stations")
		} else {
			report["stations"] = map[string]any{
				"count":              stationCount,
				"total_capacity_kg":  totalCapacity,
				"total_available_kg": totalAvailable,
			}
		}
	}

	if len(degraded) > 0 {
		report["degraded"] = degraded
	}
	return report
}

// generateAndStore persists one report for the window and returns it.
func (h *Handler) generateAndStore(ctx context.Context, days int, domain string) (*ComplianceReport, error) {
	reportJSON, err := json.Marshal(h.buildReport(ctx, days, domain))
	if err != nil {
		return nil, fmt.Errorf("marshal compliance report: %w", err)
	}
	var stored ComplianceReport
	if err := h.db.QueryRow(ctx, `
		INSERT INTO infra.compliance_reports (report) VALUES ($1::jsonb)
		RETURNING id, generated_at, report`, string(reportJSON)).
		Scan(&stored.ID, &stored.GeneratedAt, &stored.Report); err != nil {
		return nil, fmt.Errorf("persist compliance report: %w", err)
	}
	return &stored, nil
}

// GenerateScheduled stores one report for the default window and the
// fleet-config domain; used by the optional interval scheduler in main
// (COMPLIANCE_REPORT_INTERVAL).
func (h *Handler) GenerateScheduled(ctx context.Context) error {
	_, err := h.generateAndStore(ctx, defaultReportDays, defaultComplianceDomain())
	return err
}

// GenerateComplianceReport handles POST
// /v1/compliance/reports/generate?days=&domain= (Keycloak JWT). days defaults
// to 30 and is bounded to 1..365. Wave 5: domain selects the per-energy
// template pack (h2|battery|diesel|cng); default is the COMPLIANCE_DOMAIN
// fleet config, else h2.
func (h *Handler) GenerateComplianceReport(w http.ResponseWriter, r *http.Request) {
	days := defaultReportDays
	if raw := r.URL.Query().Get("days"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > maxReportDays {
			writeJSON(w, http.StatusBadRequest, map[string]string{
				"error": fmt.Sprintf("days must be an integer between 1 and %d", maxReportDays),
			})
			return
		}
		days = n
	}
	domain := defaultComplianceDomain()
	if raw := r.URL.Query().Get("domain"); raw != "" {
		if _, ok := compliancePacks[raw]; !ok {
			writeJSON(w, http.StatusBadRequest, map[string]string{
				"error": "domain must be one of h2|battery|diesel|cng",
			})
			return
		}
		domain = raw
	}
	stored, err := h.generateAndStore(r.Context(), days, domain)
	if err != nil {
		h.internal(w, "generate compliance report", err)
		return
	}
	writeJSON(w, http.StatusCreated, stored)
}
