package handlers

import (
	"encoding/json"
	"errors"
	"net/http"
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

// GenerateComplianceReport handles POST /v1/compliance/reports/generate
// (Keycloak JWT). Aggregates incident and station state into a JSON report and
// persists it in infra.compliance_reports.
func (h *Handler) GenerateComplianceReport(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	report := map[string]any{
		"generated_at": time.Now().UTC().Format(time.RFC3339),
		"standard":     "H2Fleet safety & compliance (SPEC §3.4 infra schema)",
	}

	// Incidents by status.
	incidentStatus := map[string]int{}
	rows, err := h.db.Query(ctx, `SELECT status, count(*) FROM infra.incidents GROUP BY status`)
	if err != nil {
		h.internal(w, "aggregate incidents by status", err)
		return
	}
	for rows.Next() {
		var status string
		var n int
		if err := rows.Scan(&status, &n); err != nil {
			rows.Close()
			h.internal(w, "scan incident status rollup", err)
			return
		}
		incidentStatus[status] = n
	}
	rows.Close()
	report["incidents_by_status"] = incidentStatus

	// Incidents by severity (last 30 days).
	incidentSeverity := map[string]int{}
	rows, err = h.db.Query(ctx, `
		SELECT severity, count(*) FROM infra.incidents
		WHERE opened_at > now() - interval '30 days' GROUP BY severity`)
	if err != nil {
		h.internal(w, "aggregate incidents by severity", err)
		return
	}
	for rows.Next() {
		var severity string
		var n int
		if err := rows.Scan(&severity, &n); err != nil {
			rows.Close()
			h.internal(w, "scan incident severity rollup", err)
			return
		}
		incidentSeverity[severity] = n
	}
	rows.Close()
	report["incidents_by_severity_30d"] = incidentSeverity

	// Station inventory.
	var stationCount int
	var totalCapacity, totalAvailable float64
	if err := h.db.QueryRow(ctx, `
		SELECT count(*), COALESCE(sum(capacity_kg),0), COALESCE(sum(available_kg),0)
		FROM infra.stations`).Scan(&stationCount, &totalCapacity, &totalAvailable); err != nil {
		h.internal(w, "aggregate stations", err)
		return
	}
	report["stations"] = map[string]any{
		"count":              stationCount,
		"total_capacity_kg":  totalCapacity,
		"total_available_kg": totalAvailable,
	}

	reportJSON, _ := json.Marshal(report)
	var stored ComplianceReport
	if err := h.db.QueryRow(ctx, `
		INSERT INTO infra.compliance_reports (report) VALUES ($1::jsonb)
		RETURNING id, generated_at, report`, string(reportJSON)).
		Scan(&stored.ID, &stored.GeneratedAt, &stored.Report); err != nil {
		h.internal(w, "persist compliance report", err)
		return
	}
	writeJSON(w, http.StatusCreated, stored)
}
