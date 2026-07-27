"""Wave-5 multi-energy: per-energy_type consumption/range math, unit labels,
station_type compatibility filtering/weighting, learned per-bus consumption.

Run: python -m pytest services/python/route-optimizer/tests -q
"""

from __future__ import annotations

import pytest

from app.config import settings
from app.models import Bus, Problem, Station, Stop, energy_unit, station_serves
from app.vrp import RawRoute, consumption_per_km, plan_refuels, range_km, solve_vrp

DEPOT = Stop(stop_id="depot", name="Central Depot", lat=52.50, lon=13.40)


def make_ev_bus(bus_id: str = "ev1", pct: float = 100.0,
                capacity_kwh: float = 350.0, **kw) -> Bus:
    return Bus(bus_id=bus_id, fleet_no=f"EV-{bus_id}", lat=52.50, lon=13.40,
               h2_capacity_kg=0.0, h2_level_pct=pct, energy_type="battery",
               energy_capacity=capacity_kwh, **kw)


class TestEnergyUnits:
    def test_units_per_energy_type(self):
        assert energy_unit("h2") == "kg"
        assert energy_unit("battery") == "kwh"
        assert energy_unit("diesel") == "liters"
        assert energy_unit("cng") == "kg"

    def test_bus_defaults_are_legacy_h2(self):
        bus = Bus(bus_id="b", fleet_no="H2-001", lat=52.5, lon=13.4,
                  h2_capacity_kg=40.0, h2_level_pct=50.0)
        assert bus.energy_type == "h2"
        assert bus.energy_unit == "kg"
        assert bus.capacity_units == 40.0
        assert bus.level_units == bus.h2_kg == 20.0


class TestConsumptionAndRange:
    def test_h2_default_unchanged(self):
        bus = Bus(bus_id="b", fleet_no="H2-001", lat=52.5, lon=13.4,
                  h2_capacity_kg=40.0, h2_level_pct=100.0)
        assert consumption_per_km(bus) == settings.h2_consumption_kg_per_km
        assert range_km(bus) == pytest.approx(40.0 / 0.08)

    def test_battery_range_kwh(self):
        # 70% of 350 kWh = 245 kWh at 1.1 kWh/km -> ~222.7 km.
        bus = make_ev_bus(pct=70.0)
        assert bus.energy_unit == "kwh"
        assert bus.level_units == pytest.approx(245.0)
        assert consumption_per_km(bus) == settings.battery_consumption_kwh_per_km
        assert range_km(bus) == pytest.approx(245.0 / 1.1)

    def test_diesel_range_liters(self):
        bus = Bus(bus_id="d", fleet_no="D-001", lat=52.5, lon=13.4,
                  h2_capacity_kg=0.0, h2_level_pct=50.0, energy_type="diesel",
                  energy_capacity=300.0)
        assert bus.energy_unit == "liters"
        assert range_km(bus) == pytest.approx(150.0 / settings.diesel_consumption_l_per_km)

    def test_learned_consumption_wins(self):
        # Wave-4 learned rate (fleet.fuel_consumption, 9.5 kg/100km) beats the
        # 8 kg/100km fleet default.
        bus = Bus(bus_id="b", fleet_no="H2-002", lat=52.5, lon=13.4,
                  h2_capacity_kg=40.0, h2_level_pct=100.0,
                  consumption_per_100km=9.5, consumption_source="learned")
        assert consumption_per_km(bus) == pytest.approx(0.095)
        assert range_km(bus) == pytest.approx(40.0 / 0.095)

    def test_learned_consumption_applies_to_ev(self):
        bus = make_ev_bus(consumption_per_100km=98.0, consumption_source="learned")
        assert consumption_per_km(bus) == pytest.approx(0.98)


class TestStationCompatibility:
    def test_matching_matrix(self):
        assert station_serves("h2", "h2")
        assert station_serves("ev_charger", "battery")
        assert station_serves("diesel", "diesel")
        assert station_serves("cng", "cng")
        assert station_serves("mixed", "h2")
        assert station_serves("mixed", "battery")
        assert station_serves("mixed", "diesel")
        assert station_serves("mixed", "cng")
        assert not station_serves("h2", "battery")
        assert not station_serves("ev_charger", "h2")
        assert not station_serves("diesel", "cng")

    def long_route(self, bus: Bus) -> RawRoute:
        route = RawRoute(bus=bus)
        route.stops = [
            Stop(stop_id="A", name="A", lat=52.53, lon=13.40),
            Stop(stop_id="B", name="B", lat=52.56, lon=13.40),
            Stop(stop_id="C", name="C", lat=52.70, lon=13.40),
            Stop(stop_id="D", name="D", lat=52.73, lon=13.40),
        ]
        return route

    def test_ev_bus_skips_h2_stations(self):
        # EV bus with ~44 km of range must charge en route; the only nearby
        # station is an H2 station (incompatible) -> infeasible with a note.
        bus = make_ev_bus(pct=20.0)  # 70 kWh -> ~63 km; route ~51 km + depot
        route = self.long_route(bus)
        h2_station = Station(station_id="HRS", name="H2 Hub", lat=52.56, lon=13.40,
                             available_kg=500.0, station_type="h2")
        refuels, _, feasible, notes = plan_refuels(route, [h2_station], DEPOT)
        assert not feasible
        assert notes and "compatible with battery" in notes[0]
        assert refuels == []
        assert h2_station.available_kg == 500.0  # never drawn by an EV bus

    def test_ev_bus_charges_at_ev_charger_kwh(self):
        bus = make_ev_bus(pct=20.0)
        route = self.long_route(bus)
        # Charger sits at C (52.70): reachable when the D->depot leg (25.6 km)
        # would breach the safety range.
        charger = Station(station_id="EV1", name="C chargers", lat=52.70, lon=13.40,
                          available_kg=0.0, station_type="ev_charger",
                          available_kwh=500.0)
        refuels, energy_end, feasible, notes = plan_refuels(route, [charger], DEPOT)
        assert feasible, notes
        assert refuels and all(r.station_id == "EV1" for r in refuels)
        taken = sum(r.kg_taken for r in refuels)
        assert taken > 0
        # kWh inventory decremented in kWh; event is unit-labeled.
        assert charger.available_kwh == pytest.approx(500.0 - taken, abs=0.01)
        assert all(r.energy_unit == "kwh" for r in refuels)
        assert all(r.station_type == "ev_charger" for r in refuels)
        assert energy_end >= 0

    def test_mixed_station_serves_but_is_weighted(self):
        # Same-distance trade: a dedicated ev_charger slightly farther than a
        # 'mixed' station still wins below the detour factor; beyond it the
        # mixed station wins. (Refuel decision happens at D, 52.73.)
        bus = make_ev_bus(pct=20.0)
        route = self.long_route(bus)
        # Mixed at 52.731 (~0.1 km), dedicated at 52.732 (~0.2 km):
        # weighted 0.1*1.25=0.125 < 0.2 -> mixed wins on closeness.
        mixed = Station(station_id="MIX", name="Mixed hub", lat=52.731, lon=13.40,
                        available_kg=0.0, station_type="mixed", available_kwh=400.0)
        ded = Station(station_id="EVX", name="Dedicated", lat=52.732, lon=13.40,
                      available_kg=0.0, station_type="ev_charger", available_kwh=400.0)
        refuels, _, feasible, notes = plan_refuels(route, [mixed, ded], DEPOT)
        assert feasible, notes
        assert refuels[0].station_id == "MIX"

        # Dedicated clearly closer than mixed/factor -> dedicated wins.
        mixed2 = Station(station_id="MIX2", name="Mixed hub", lat=52.733, lon=13.40,
                         available_kg=0.0, station_type="mixed", available_kwh=400.0)
        ded2 = Station(station_id="EVX2", name="Dedicated", lat=52.731, lon=13.40,
                       available_kg=0.0, station_type="ev_charger", available_kwh=400.0)
        bus2 = make_ev_bus(pct=20.0)
        refuels2, _, feasible2, notes2 = plan_refuels(self.long_route(bus2), [mixed2, ded2], DEPOT)
        assert feasible2, notes2
        assert refuels2[0].station_id == "EVX2"


class TestMixedFleetVrp:
    def test_mixed_fleet_solves(self):
        h2 = Bus(bus_id="001", fleet_no="H2-001", lat=52.50, lon=13.40,
                 h2_capacity_kg=40.0, h2_level_pct=100.0)
        ev = make_ev_bus("ev1", pct=90.0)
        problem = Problem(
            depot=DEPOT,
            buses=[h2, ev],
            stations=[],
            stops=[Stop(stop_id=f"S{i:02d}", name=f"Stop {i}",
                        lat=52.51 + 0.004 * (i % 3), lon=13.41 + 0.004 * (i // 3))
                   for i in range(6)],
        )
        routes, dropped, status = solve_vrp(problem)
        assert status in ("SUCCESS", "OPTIMAL", "PARTIAL_SUCCESS")
        assert dropped == []
        assigned = [s.stop_id for r in routes for s in r.stops]
        assert sorted(assigned) == [f"S{i:02d}" for i in range(6)]
        for r in routes:
            if r.stops:
                assert r.km <= range_km(r.bus) + 1e-6
