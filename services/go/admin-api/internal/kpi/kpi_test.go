package kpi

import (
	"context"
	"errors"
	"testing"
	"time"
)

type stubSource struct {
	name string
	val  any
	err  error
	delay time.Duration
}

func (s stubSource) Name() string { return s.name }
func (s stubSource) Collect(ctx context.Context) (any, error) {
	if s.delay > 0 {
		select {
		case <-time.After(s.delay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return s.val, s.err
}

func TestCollectMergesAllSources(t *testing.T) {
	agg := NewAggregator([]Source{
		stubSource{name: "fleet", val: FleetKPIs{VehiclesTotal: 50, VehiclesAvailable: 44, TelemetryPointsPerMin: 120}},
		stubSource{name: "infra", val: InfraKPIs{OpenIncidents: 2}},
		stubSource{name: "citizen", val: CitizenKPIs{DRTRequestsToday: 7, CarbonKgCO2Total: 1234.5}},
		stubSource{name: "commerce", val: CommerceKPIs{Payments30d: 12, Revenue30dMinor: 45600, Currency: "EUR"}},
		stubSource{name: "toggles", val: ToggleKPIs{ModulesEnabled: 18, ModulesTotal: 20,
			Domains: map[string]DomainCount{"fleet": {Enabled: 4, Total: 5}}}},
	}, time.Second)

	k := agg.Collect(context.Background())
	if k.Fleet == nil || k.Fleet.VehiclesAvailable != 44 {
		t.Fatalf("fleet section wrong: %+v", k.Fleet)
	}
	if k.Infra == nil || k.Infra.OpenIncidents != 2 {
		t.Fatalf("infra section wrong: %+v", k.Infra)
	}
	if k.Citizen == nil || k.Citizen.DRTRequestsToday != 7 {
		t.Fatalf("citizen section wrong: %+v", k.Citizen)
	}
	if k.Commerce == nil || k.Commerce.Revenue30dMinor != 45600 {
		t.Fatalf("commerce section wrong: %+v", k.Commerce)
	}
	if k.Toggles == nil || k.Toggles.ModulesEnabled != 18 {
		t.Fatalf("toggles section wrong: %+v", k.Toggles)
	}
	if k.Meta.Partial || len(k.Meta.Degraded) != 0 {
		t.Fatalf("no degradation expected: %+v", k.Meta)
	}
	if k.GeneratedAt.IsZero() {
		t.Fatalf("generated_at not stamped")
	}
}

func TestCollectDegradesFailedSource(t *testing.T) {
	agg := NewAggregator([]Source{
		stubSource{name: "fleet", val: FleetKPIs{VehiclesTotal: 50}},
		stubSource{name: "infra", err: errors.New("db down")},
	}, time.Second)

	k := agg.Collect(context.Background())
	if k.Fleet == nil {
		t.Fatalf("healthy source must still be collected")
	}
	if k.Infra != nil {
		t.Fatalf("failed source must leave its section nil")
	}
	if !k.Meta.Partial || len(k.Meta.Degraded) != 1 || k.Meta.Degraded[0] != "infra" {
		t.Fatalf("degradation metadata wrong: %+v", k.Meta)
	}
}

func TestCollectTimesOutSlowSource(t *testing.T) {
	agg := NewAggregator([]Source{
		stubSource{name: "fleet", val: FleetKPIs{VehiclesTotal: 50}},
		stubSource{name: "commerce", delay: 500 * time.Millisecond},
	}, 50*time.Millisecond)

	start := time.Now()
	k := agg.Collect(context.Background())
	elapsed := time.Since(start)

	if k.Commerce != nil {
		t.Fatalf("timed-out source must leave its section nil")
	}
	if len(k.Meta.Degraded) != 1 || k.Meta.Degraded[0] != "commerce" {
		t.Fatalf("timeout must mark the source degraded: %+v", k.Meta)
	}
	if elapsed > 400*time.Millisecond {
		t.Fatalf("aggregation must be bounded by the timeout, took %v", elapsed)
	}
}
