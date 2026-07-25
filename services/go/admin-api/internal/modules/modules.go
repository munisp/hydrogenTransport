// Package modules is the static registry of the 20 H2Fleet capability
// modules (SPEC §3.1): their domain and the services that own/implement them.
// Used by the admin toggle view (enrichment) and the KPI aggregation
// (per-domain enabled counts).
package modules

// Info describes one toggleable module.
type Info struct {
	Domain         string   `json:"domain"`
	OwningServices []string `json:"owning_services"`
}

// Registry maps module id -> domain + owning services (SPEC §3.1/§3.6).
var Registry = map[string]Info{
	// Domain 1 — Fleet Operations & Telematics
	"telematics":             {"fleet", []string{"fleet-api", "telemetry-ingest"}},
	"predictive-maintenance": {"fleet", []string{"fleet-api", "predictive-maintenance"}},
	"digital-twin":           {"fleet", []string{"fleet-api", "digital-twin"}},
	"fuel-monitoring":        {"fleet", []string{"fleet-api", "telemetry-ingest"}},
	"route-energy-optimizer": {"fleet", []string{"fleet-api", "route-optimizer"}},
	// Domain 2 — Infrastructure & Safety
	"refueling-stations":   {"infra", []string{"infra-api"}},
	"leak-detection":       {"infra", []string{"infra-api"}},
	"dispatch-workforce":   {"infra", []string{"infra-api"}},
	"compliance-reporting": {"infra", []string{"infra-api"}},
	"depot-management":     {"infra", []string{"infra-api"}},
	// Domain 3 — Citizen & Engagement
	"passenger-pwa":     {"citizen", []string{"citizen-api", "pwa"}},
	"mobile-app":        {"citizen", []string{"citizen-api", "mobile-app"}},
	"demand-responsive": {"citizen", []string{"citizen-api"}},
	"carbon-credits":    {"citizen", []string{"citizen-api", "carbon-analytics"}},
	"open-data-portal":  {"citizen", []string{"citizen-api"}},
	// Domain 4 — Commerce & Finance
	"fare-payments":       {"commerce", []string{"commerce-api"}},
	"loyalty-marketplace": {"commerce", []string{"commerce-api"}},
	"energy-trading":      {"commerce", []string{"commerce-api"}},
	"gov-dashboard":       {"commerce", []string{"commerce-api", "pwa"}},
	"advertising":         {"commerce", []string{"commerce-api"}},
}

// Domains lists the four domain ids in stable order.
var Domains = []string{"fleet", "infra", "citizen", "commerce"}

// DomainOf returns the domain for a module id ("unknown" when unregistered).
func DomainOf(module string) string {
	if info, ok := Registry[module]; ok {
		return info.Domain
	}
	return "unknown"
}
