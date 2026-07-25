// Package kpi implements the unified cross-service KPI aggregation of
// admin-api: GET /v1/admin/kpis fans out to per-domain sources (fleet,
// infra, citizen, commerce, toggles), each with a timeout, and merges
// whatever succeeded into one payload with explicit degradation metadata.
package kpi

import (
	"context"
	"sync"
	"time"
)

// DefaultTimeout bounds each source collection.
const DefaultTimeout = 3 * time.Second

// Source is one KPI contributor (a domain schema query or an HTTP call).
type Source interface {
	// Name identifies the payload section ("fleet", "infra", "citizen",
	// "commerce", "toggles").
	Name() string
	Collect(ctx context.Context) (any, error)
}

// FleetKPIs is the Domain-1 section.
type FleetKPIs struct {
	VehiclesTotal         int     `json:"vehicles_total"`
	VehiclesAvailable     int     `json:"vehicles_available"`
	TelemetryPointsPerMin float64 `json:"telemetry_points_per_min"`
}

// InfraKPIs is the Domain-2 section.
type InfraKPIs struct {
	OpenIncidents int `json:"open_incidents"`
}

// CitizenKPIs is the Domain-3 section.
type CitizenKPIs struct {
	DRTRequestsToday  int     `json:"drt_requests_today"`
	CarbonKgCO2Total  float64 `json:"carbon_kg_co2_total"`
}

// CommerceKPIs is the Domain-4 section.
type CommerceKPIs struct {
	Payments30d     int    `json:"payments_30d"`
	Revenue30dMinor int64  `json:"revenue_30d_minor"`
	Currency        string `json:"currency"`
}

// DomainCount tallies module enablement inside one domain.
type DomainCount struct {
	Enabled int `json:"enabled"`
	Total   int `json:"total"`
}

// ToggleKPIs is the toggle-service section: per-domain enabled counts.
type ToggleKPIs struct {
	ModulesEnabled int                    `json:"modules_enabled"`
	ModulesTotal   int                    `json:"modules_total"`
	Domains        map[string]DomainCount `json:"domains"`
}

// Meta describes aggregation health.
type Meta struct {
	Partial  bool     `json:"partial"`
	Degraded []string `json:"degraded"`
}

// KPIs is the GET /v1/admin/kpis payload. Sections are pointers: a nil
// section means its source failed or timed out (see Meta.Degraded).
type KPIs struct {
	GeneratedAt time.Time   `json:"generated_at"`
	Fleet       *FleetKPIs  `json:"fleet"`
	Infra       *InfraKPIs  `json:"infra"`
	Citizen     *CitizenKPIs `json:"citizen"`
	Commerce    *CommerceKPIs `json:"commerce"`
	Toggles     *ToggleKPIs `json:"toggles"`
	Meta        Meta        `json:"meta"`
}

// Aggregator fans out to the sources concurrently and merges results.
type Aggregator struct {
	sources []Source
	timeout time.Duration
}

// NewAggregator wires the aggregator; timeout <= 0 falls back to
// DefaultTimeout.
func NewAggregator(sources []Source, timeout time.Duration) *Aggregator {
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	return &Aggregator{sources: sources, timeout: timeout}
}

type result struct {
	name string
	val  any
	err  error
}

// Collect runs every source concurrently (each bounded by the aggregator
// timeout) and merges the successes. Failed/timed-out sources leave their
// section nil and are named in Meta.Degraded.
func (a *Aggregator) Collect(ctx context.Context) KPIs {
	out := KPIs{GeneratedAt: time.Now().UTC(), Meta: Meta{Degraded: []string{}}}
	ch := make(chan result, len(a.sources))
	for _, src := range a.sources {
		go func(src Source) {
			sctx, cancel := context.WithTimeout(ctx, a.timeout)
			defer cancel()
			val, err := src.Collect(sctx)
			ch <- result{name: src.Name(), val: val, err: err}
		}(src)
	}
	for range a.sources {
		res := <-ch
		if res.err != nil {
			out.Meta.Degraded = append(out.Meta.Degraded, res.name)
			continue
		}
		switch v := res.val.(type) {
		case FleetKPIs:
			out.Fleet = &v
		case InfraKPIs:
			out.Infra = &v
		case CitizenKPIs:
			out.Citizen = &v
		case CommerceKPIs:
			out.Commerce = &v
		case ToggleKPIs:
			out.Toggles = &v
		}
	}
	out.Meta.Partial = len(out.Meta.Degraded) > 0
	return out
}
