// Package kpi implements the unified cross-service KPI aggregation behind
// GET /v1/admin/kpis. Each KPI section is collected by an independent Source;
// sources run concurrently with a 3s timeout each and failures degrade
// partially: the failed section is returned as null and named in
// meta.degraded (SPEC: partial-degradation fan-out).
package kpi

import (
	"context"
	"sort"
	"sync"
	"time"
)

// DefaultTimeout is the per-source collection timeout (3s per SPEC).
const DefaultTimeout = 3 * time.Second

// Source collects one KPI section. Returned values must be JSON-marshalable.
type Source interface {
	Name() string
	Collect(ctx context.Context) (any, error)
}

// FleetKPIs is the Domain-1 section (fleet-api / Postgres fleet schema).
type FleetKPIs struct {
	VehiclesTotal         int     `json:"vehicles_total"`
	VehiclesAvailable     int     `json:"vehicles_available"` // status = 'active'
	TelemetryPointsPerMin float64 `json:"telemetry_points_per_min"`
}

// InfraKPIs is the Domain-2 section (infra-api / Postgres infra schema).
type InfraKPIs struct {
	OpenIncidents int `json:"open_incidents"` // status in (open, acknowledged)
}

// CitizenKPIs is the Domain-3 section (citizen-api / Postgres citizen schema).
type CitizenKPIs struct {
	DRTRequestsToday int     `json:"drt_requests_today"`
	CarbonKgCO2Total float64 `json:"carbon_kg_co2_total"`
}

// CommerceKPIs is the Domain-4 section (commerce-api / Postgres commerce schema).
type CommerceKPIs struct {
	Payments30d     int    `json:"payments_30d"`
	Revenue30dMinor int64  `json:"revenue_30d_minor"` // settled fares, minor units
	Currency        string `json:"currency"`
}

// DomainCount is the per-domain toggle summary.
type DomainCount struct {
	Enabled int `json:"enabled"`
	Total   int `json:"total"`
}

// ToggleKPIs summarizes feature-toggle coverage across the 20 modules.
type ToggleKPIs struct {
	ModulesEnabled int                    `json:"modules_enabled"`
	ModulesTotal   int                    `json:"modules_total"`
	Domains        map[string]DomainCount `json:"domains"`
}

// Meta reports degradation of the aggregated response.
type Meta struct {
	Partial  bool     `json:"partial"`
	Degraded []string `json:"degraded"` // names of sources that failed/timed out
}

// Response is the GET /v1/admin/kpis payload. Sections are null when their
// source failed (see meta.degraded).
type Response struct {
	GeneratedAt time.Time `json:"generated_at"`
	Fleet       any       `json:"fleet"`
	Infra       any       `json:"infra"`
	Citizen     any       `json:"citizen"`
	Commerce    any       `json:"commerce"`
	Toggles     any       `json:"toggles"`
	Meta        Meta      `json:"meta"`
}

// Aggregator fans out to the KPI sources concurrently.
type Aggregator struct {
	sources []Source
	timeout time.Duration
}

// NewAggregator builds an Aggregator; timeout<=0 uses DefaultTimeout (3s).
func NewAggregator(sources []Source, timeout time.Duration) *Aggregator {
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	return &Aggregator{sources: sources, timeout: timeout}
}

type result struct {
	name string
	data any
	err  error
}

// Collect runs all sources concurrently and assembles the Response. It never
// fails as a whole: individual failures produce null sections plus a
// meta.degraded entry.
func (a *Aggregator) Collect(ctx context.Context) Response {
	results := make(chan result, len(a.sources))
	var wg sync.WaitGroup
	for _, src := range a.sources {
		wg.Add(1)
		go func(s Source) {
			defer wg.Done()
			sctx, cancel := context.WithTimeout(ctx, a.timeout)
			defer cancel()
			data, err := s.Collect(sctx)
			results <- result{name: s.Name(), data: data, err: err}
		}(src)
	}
	wg.Wait()
	close(results)

	resp := Response{GeneratedAt: time.Now().UTC(), Meta: Meta{Degraded: []string{}}}
	for r := range results {
		// A source that failed OR returned no data without an error leaves
		// its section null; either way it is named in meta.degraded so a
		// missing value is never mistaken for a real zero.
		if r.err != nil || r.data == nil {
			resp.Meta.Degraded = append(resp.Meta.Degraded, r.name)
			continue
		}
		switch r.name {
		case "fleet":
			resp.Fleet = r.data
		case "infra":
			resp.Infra = r.data
		case "citizen":
			resp.Citizen = r.data
		case "commerce":
			resp.Commerce = r.data
		case "toggles":
			resp.Toggles = r.data
		}
	}
	sort.Strings(resp.Meta.Degraded)
	resp.Meta.Partial = len(resp.Meta.Degraded) > 0
	return resp
}
