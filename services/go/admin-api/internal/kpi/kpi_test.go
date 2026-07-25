package kpi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

type fakeSource struct {
	name  string
	data  any
	err   error
	delay time.Duration
}

func (f fakeSource) Name() string { return f.name }
func (f fakeSource) Collect(ctx context.Context) (any, error) {
	if f.delay > 0 {
		select {
		case <-time.After(f.delay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return f.data, f.err
}

func okSources() []Source {
	return []Source{
		fakeSource{name: "fleet", data: FleetKPIs{VehiclesTotal: 50, VehiclesAvailable: 44, TelemetryPointsPerMin: 120}},
		fakeSource{name: "infra", data: InfraKPIs{OpenIncidents: 2}},
		fakeSource{name: "citizen", data: CitizenKPIs{DRTRequestsToday: 7, CarbonKgCO2Total: 1234.5}},
		fakeSource{name: "commerce", data: CommerceKPIs{Payments30d: 12, Revenue30dMinor: 45600, Currency: "EUR"}},
		fakeSource{name: "toggles", data: ToggleKPIs{ModulesEnabled: 20, ModulesTotal: 20}},
	}
}

func TestAggregateAllSourcesOK(t *testing.T) {
	agg := NewAggregator(okSources(), time.Second)
	resp := agg.Collect(context.Background())
	if resp.Meta.Partial || len(resp.Meta.Degraded) != 0 {
		t.Fatalf("expected no degradation, got %+v", resp.Meta)
	}
	for name, section := range map[string]any{
		"fleet": resp.Fleet, "infra": resp.Infra, "citizen": resp.Citizen,
		"commerce": resp.Commerce, "toggles": resp.Toggles,
	} {
		if section == nil {
			t.Fatalf("section %s must not be null", name)
		}
	}
	fleet := resp.Fleet.(FleetKPIs)
	if fleet.VehiclesAvailable != 44 {
		t.Fatalf("unexpected fleet KPIs: %+v", fleet)
	}
	// The whole response must marshal to the documented JSON shape.
	buf, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(buf, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if m["generated_at"] == nil || m["meta"] == nil {
		t.Fatalf("missing top-level keys: %s", buf)
	}
}

func TestAggregatePartialDegradation(t *testing.T) {
	sources := okSources()
	sources[3] = fakeSource{name: "commerce", err: errors.New("postgres down")}
	agg := NewAggregator(sources, time.Second)
	resp := agg.Collect(context.Background())

	if !resp.Meta.Partial {
		t.Fatalf("expected partial response")
	}
	if len(resp.Meta.Degraded) != 1 || resp.Meta.Degraded[0] != "commerce" {
		t.Fatalf("expected degraded=[commerce], got %v", resp.Meta.Degraded)
	}
	if resp.Commerce != nil {
		t.Fatalf("failed section must be null, got %+v", resp.Commerce)
	}
	if resp.Fleet == nil || resp.Infra == nil || resp.Citizen == nil || resp.Toggles == nil {
		t.Fatalf("healthy sections must still be populated")
	}
}

// A source that silently returns (nil, nil) must still surface as a null
// section plus a degraded entry — never as a plausible-looking zero value.
func TestAggregateNilDataDegrades(t *testing.T) {
	sources := []Source{
		fakeSource{name: "fleet", data: FleetKPIs{VehiclesTotal: 50}},
		fakeSource{name: "infra", data: nil, err: nil}, // silent no-data
	}
	agg := NewAggregator(sources, time.Second)
	resp := agg.Collect(context.Background())
	if !resp.Meta.Partial || len(resp.Meta.Degraded) != 1 || resp.Meta.Degraded[0] != "infra" {
		t.Fatalf("nil-data source must degrade: %+v", resp.Meta)
	}
	if resp.Infra != nil {
		t.Fatalf("nil-data section must be null, got %+v", resp.Infra)
	}
	if resp.Fleet == nil {
		t.Fatalf("healthy section must be populated")
	}
}

func TestAggregateSourceTimeout(t *testing.T) {
	sources := []Source{
		fakeSource{name: "fleet", data: FleetKPIs{VehiclesTotal: 50}},
		fakeSource{name: "infra", delay: 500 * time.Millisecond}, // exceeds timeout
	}
	agg := NewAggregator(sources, 50*time.Millisecond)
	start := time.Now()
	resp := agg.Collect(context.Background())
	if elapsed := time.Since(start); elapsed > 400*time.Millisecond {
		t.Fatalf("aggregation blocked past the source timeout: %v", elapsed)
	}
	if resp.Infra != nil || len(resp.Meta.Degraded) != 1 || resp.Meta.Degraded[0] != "infra" {
		t.Fatalf("slow source must degrade: %+v degraded=%v", resp.Infra, resp.Meta.Degraded)
	}
	if resp.Fleet == nil {
		t.Fatalf("fast source must succeed")
	}
}

func TestToggleSourceCountsPerDomain(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/toggles" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"toggles": {
			"telematics": true, "digital-twin": false,
			"refueling-stations": true, "leak-detection": true,
			"fare-payments": true
		}}`))
	}))
	defer srv.Close()

	src := ToggleSource(srv.URL)
	data, err := src.Collect(context.Background())
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	k := data.(ToggleKPIs)
	if k.ModulesTotal != 5 || k.ModulesEnabled != 4 {
		t.Fatalf("bad totals: %+v", k)
	}
	if got := k.Domains["fleet"]; got.Total != 2 || got.Enabled != 1 {
		t.Fatalf("bad fleet domain count: %+v", got)
	}
	if got := k.Domains["infra"]; got.Total != 2 || got.Enabled != 2 {
		t.Fatalf("bad infra domain count: %+v", got)
	}
	if got := k.Domains["commerce"]; got.Total != 1 || got.Enabled != 1 {
		t.Fatalf("bad commerce domain count: %+v", got)
	}
	if got := k.Domains["citizen"]; got.Total != 0 {
		t.Fatalf("citizen domain should be zero-valued: %+v", got)
	}
}

func TestToggleSourceFailureDegrades(t *testing.T) {
	// Unreachable toggle-service -> Collect must return an error so the
	// aggregator marks the toggles section degraded.
	src := ToggleSource("http://127.0.0.1:1")
	if _, err := src.Collect(context.Background()); err == nil {
		t.Fatalf("expected error for unreachable toggle-service")
	}
}
