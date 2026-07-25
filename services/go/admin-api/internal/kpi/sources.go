package kpi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/munisp/hydrogenTransport/services/go/admin-api/internal/modules"
)

// ---------------------------------------------------------------------------
// Postgres-backed sources (the H2Fleet services share one database; admin-api
// reads the per-domain schemas directly for aggregate KPIs).
// ---------------------------------------------------------------------------

type pgSource struct {
	name string
	pool *pgxpool.Pool
	fn   func(ctx context.Context, pool *pgxpool.Pool) (any, error)
}

func (s *pgSource) Name() string { return s.name }
func (s *pgSource) Collect(ctx context.Context) (any, error) {
	return s.fn(ctx, s.pool)
}

// FleetSource aggregates Domain-1 KPIs: fleet availability (active vehicles)
// and the telemetry ingestion rate (points/min over the last 5 minutes).
func FleetSource(pool *pgxpool.Pool) Source {
	return &pgSource{name: "fleet", pool: pool, fn: func(ctx context.Context, pool *pgxpool.Pool) (any, error) {
		var k FleetKPIs
		err := pool.QueryRow(ctx, `
SELECT count(*), count(*) FILTER (WHERE status = 'active') FROM fleet.vehicles`).
			Scan(&k.VehiclesTotal, &k.VehiclesAvailable)
		if err != nil {
			return nil, fmt.Errorf("fleet availability: %w", err)
		}
		var points int
		err = pool.QueryRow(ctx, `
SELECT count(*) FROM fleet.telemetry WHERE ts >= now() - interval '5 minutes'`).Scan(&points)
		if err != nil {
			return nil, fmt.Errorf("telemetry rate: %w", err)
		}
		k.TelemetryPointsPerMin = float64(points) / 5.0
		return k, nil
	}}
}

// InfraSource aggregates Domain-2 KPIs: open (open + acknowledged) incidents.
func InfraSource(pool *pgxpool.Pool) Source {
	return &pgSource{name: "infra", pool: pool, fn: func(ctx context.Context, pool *pgxpool.Pool) (any, error) {
		var k InfraKPIs
		err := pool.QueryRow(ctx, `
SELECT count(*) FROM infra.incidents WHERE status IN ('open','acknowledged')`).Scan(&k.OpenIncidents)
		if err != nil {
			return nil, fmt.Errorf("open incidents: %w", err)
		}
		return k, nil
	}}
}

// CitizenSource aggregates Domain-3 KPIs: DRT requests today and total CO2
// avoided accounted by the carbon-credits module.
func CitizenSource(pool *pgxpool.Pool) Source {
	return &pgSource{name: "citizen", pool: pool, fn: func(ctx context.Context, pool *pgxpool.Pool) (any, error) {
		var k CitizenKPIs
		err := pool.QueryRow(ctx, `
SELECT count(*) FROM citizen.drt_requests WHERE requested_at >= date_trunc('day', now())`).
			Scan(&k.DRTRequestsToday)
		if err != nil {
			return nil, fmt.Errorf("drt requests today: %w", err)
		}
		err = pool.QueryRow(ctx, `
SELECT coalesce(sum(kg_co2_avoided), 0)::float8 FROM citizen.carbon_credits`).Scan(&k.CarbonKgCO2Total)
		if err != nil {
			return nil, fmt.Errorf("carbon total: %w", err)
		}
		return k, nil
	}}
}

// CommerceSource aggregates Domain-4 KPIs: payments in the last 30 days and
// settled revenue (minor units) over the same window.
func CommerceSource(pool *pgxpool.Pool) Source {
	return &pgSource{name: "commerce", pool: pool, fn: func(ctx context.Context, pool *pgxpool.Pool) (any, error) {
		var k CommerceKPIs
		err := pool.QueryRow(ctx, `
SELECT count(*), coalesce(sum(amount_minor) FILTER (WHERE status = 'settled'), 0)
FROM commerce.fare_payments
WHERE created_at >= now() - interval '30 days'`).Scan(&k.Payments30d, &k.Revenue30dMinor)
		if err != nil {
			return nil, fmt.Errorf("payments 30d: %w", err)
		}
		var currency *string
		err = pool.QueryRow(ctx, `
SELECT currency FROM commerce.fare_payments
WHERE created_at >= now() - interval '30 days'
ORDER BY created_at DESC LIMIT 1`).Scan(&currency)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("payments currency: %w", err)
		}
		if currency != nil {
			k.Currency = *currency
		} else {
			k.Currency = "EUR"
		}
		return k, nil
	}}
}

// ---------------------------------------------------------------------------
// Toggle-service source (HTTP): per-domain module enabled counts.
// ---------------------------------------------------------------------------

type toggleSource struct {
	baseURL string
	http    *http.Client
}

// ToggleSource returns a Source counting enabled modules per domain via the
// toggle-service REST API (GET /v1/toggles, SPEC §3.2).
func ToggleSource(toggleURL string) Source {
	return &toggleSource{baseURL: toggleURL, http: &http.Client{Timeout: 3 * time.Second}}
}

func (s *toggleSource) Name() string { return "toggles" }

func (s *toggleSource) Collect(ctx context.Context) (any, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.baseURL+"/v1/toggles", nil)
	if err != nil {
		return nil, err
	}
	resp, err := s.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("toggle-service: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("toggle-service: status %d", resp.StatusCode)
	}
	var body struct {
		Toggles map[string]bool `json:"toggles"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, fmt.Errorf("toggle-service decode: %w", err)
	}
	k := ToggleKPIs{Domains: map[string]DomainCount{}}
	for _, domain := range modules.Domains {
		k.Domains[domain] = DomainCount{}
	}
	for module, enabled := range body.Toggles {
		domain := modules.DomainOf(module)
		c := k.Domains[domain]
		c.Total++
		k.ModulesTotal++
		if enabled {
			c.Enabled++
			k.ModulesEnabled++
		}
		k.Domains[domain] = c
	}
	return k, nil
}
