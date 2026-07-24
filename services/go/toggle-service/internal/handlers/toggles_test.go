package handlers

import "testing"

// SPEC §3.1 is a sacred contract: the toggle-service registry must contain
// exactly these 20 module identifiers, each mapped to its domain. The gate
// middleware in every other service resolves toggles through this registry,
// so a drift here silently changes 404 behaviour platform-wide.
func TestModulesMatchSpec(t *testing.T) {
	want := map[string]string{
		// Domain 1 — fleet
		"telematics":             "fleet",
		"predictive-maintenance": "fleet",
		"digital-twin":           "fleet",
		"fuel-monitoring":        "fleet",
		"route-energy-optimizer": "fleet",
		// Domain 2 — infra
		"refueling-stations":   "infra",
		"leak-detection":       "infra",
		"dispatch-workforce":   "infra",
		"compliance-reporting": "infra",
		"depot-management":     "infra",
		// Domain 3 — citizen
		"passenger-pwa":     "citizen",
		"mobile-app":        "citizen",
		"demand-responsive": "citizen",
		"carbon-credits":    "citizen",
		"open-data-portal":  "citizen",
		// Domain 4 — commerce
		"fare-payments":       "commerce",
		"loyalty-marketplace": "commerce",
		"energy-trading":      "commerce",
		"gov-dashboard":       "commerce",
		"advertising":         "commerce",
	}

	if len(Modules) != len(want) {
		t.Fatalf("Modules has %d entries, want %d (SPEC §3.1)", len(Modules), len(want))
	}
	for module, domain := range want {
		got, ok := Modules[module]
		if !ok {
			t.Errorf("module %q missing from registry", module)
			continue
		}
		if got != domain {
			t.Errorf("module %q: domain %q, want %q", module, got, domain)
		}
	}
	for module := range Modules {
		if _, ok := want[module]; !ok {
			t.Errorf("unexpected module %q in registry (not in SPEC §3.1)", module)
		}
	}
}
