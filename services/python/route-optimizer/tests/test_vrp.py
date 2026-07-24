"""Tests for the VRP solver and the deterministic refuel planner.

Run: python -m pytest services/python/route-optimizer/tests -q
"""

from __future__ import annotations

import pytest

from app.config import settings
from app.models import Bus, Problem, Station, Stop
from app.vrp import RawRoute, haversine_km, plan_refuels, range_km, solve_vrp

DEPOT = Stop(stop_id="depot", name="Central Depot", lat=52.50, lon=13.40)


def make_bus(bus_id: str, h2_pct: float, lat: float = 52.50, lon: float = 13.40) -> Bus:
    return Bus(
        bus_id=bus_id,
        fleet_no=f"H2-{bus_id}",
        lat=lat,
        lon=lon,
        h2_capacity_kg=40.0,
        h2_level_pct=h2_pct,
    )


def ring_of_stops(n: int, lat0: float = 52.51, lon0: float = 13.41) -> list[Stop]:
    return [
        Stop(stop_id=f"S{i:02d}", name=f"Stop {i}", lat=lat0 + 0.004 * (i % 3), lon=lon0 + 0.004 * (i // 3))
        for i in range(n)
    ]


class TestHaversine:
    def test_zero_distance(self):
        assert haversine_km(52.5, 13.4, 52.5, 13.4) == 0.0

    def test_known_distance(self):
        # 1 degree of latitude ≈ 111.2 km.
        assert haversine_km(52.0, 13.0, 53.0, 13.0) == pytest.approx(111.19, rel=0.01)

    def test_symmetry(self):
        assert haversine_km(52.5, 13.4, 52.6, 13.5) == pytest.approx(
            haversine_km(52.6, 13.5, 52.5, 13.4)
        )


class TestSolveVrp:
    def test_feasible_all_stops_assigned(self):
        problem = Problem(
            depot=DEPOT,
            buses=[make_bus("001", 100.0), make_bus("002", 90.0, lat=52.52, lon=13.42)],
            stations=[],
            stops=ring_of_stops(6),
        )
        routes, dropped, status = solve_vrp(problem)

        assert status in ("SUCCESS", "OPTIMAL", "PARTIAL_SUCCESS")
        assert dropped == []
        assigned = [s.stop_id for r in routes for s in r.stops]
        assert sorted(assigned) == [f"S{i:02d}" for i in range(6)]
        # Every route ends within the bus's H2 range budget (phase-1 capacity).
        for r in routes:
            if r.stops:
                assert r.km > 0
                max_km = range_km(r.bus) + 40.0 / settings.h2_consumption_kg_per_km
                assert r.km <= max_km + 1e-6

    def test_unservable_stops_are_dropped_not_crashed(self):
        # A nearly-empty bus far from a cluster cannot serve it; the solver
        # must drop stops (with penalty) instead of failing or violating range.
        problem = Problem(
            depot=DEPOT,
            buses=[make_bus("001", 2.0)],  # 0.8 kg ≈ 10 km range
            stations=[],
            stops=ring_of_stops(4, lat0=52.90, lon0=14.10),  # ~60 km away
        )
        routes, dropped, status = solve_vrp(problem)
        assert status != "INVALID"
        assert len(dropped) == 4
        assert all(len(r.stops) == 0 for r in routes)


class TestPlanRefuels:
    """Route geometry (all lon=13.40, legs in km via ~0.009°/km latitude):

        depot 52.50 ─3.3─ A 52.53 ─3.3─ B 52.56 ─15.6─ C 52.70 ─3.3─ D 52.73
        D ─25.6─ depot

    Total ≈ 51 km. The long B→C leg forces a refuel decision at B for a bus
    with ~40 km of range (8% of 40 kg at 0.08 kg/km), with ~13 km of
    reachability margin above the 20 km safety buffer.
    """

    def long_route(self, h2_pct: float, bus_id: str = "001") -> RawRoute:
        bus = make_bus(bus_id, h2_pct)
        route = RawRoute(bus=bus)
        route.stops = [
            Stop(stop_id="A", name="A", lat=52.53, lon=13.40),
            Stop(stop_id="B", name="B", lat=52.56, lon=13.40),
            Stop(stop_id="C", name="C", lat=52.70, lon=13.40),
            Stop(stop_id="D", name="D", lat=52.73, lon=13.40),
        ]
        return route

    def test_short_route_needs_no_refuel(self):
        route = self.long_route(100.0)  # 500 km range ≫ 51 km route
        station = Station(station_id="ST1", name="Hub", lat=52.56, lon=13.40, available_kg=200.0)
        refuels, h2_end, feasible, notes = plan_refuels(route, [station], DEPOT)
        assert feasible, notes
        assert refuels == []
        assert station.available_kg == 200.0  # untouched
        assert h2_end > 0

    def test_station_inventory_decremented(self):
        route = self.long_route(8.0)  # 3.2 kg ≈ 40 km range — refuel needed at B
        station = Station(station_id="ST1", name="Hub", lat=52.56, lon=13.40, available_kg=100.0)
        start_inventory = station.available_kg

        refuels, h2_end, feasible, notes = plan_refuels(route, [station], DEPOT)

        assert feasible, f"should be feasible with a stocked station: {notes}"
        assert len(refuels) >= 1
        taken = sum(r.kg_taken for r in refuels)
        assert taken > 0
        # Inventory bookkeeping: the station lost what the bus took (kg_taken
        # is rounded to 2dp in the event, hence the small tolerance).
        assert station.available_kg == pytest.approx(start_inventory - taken, abs=0.01)
        assert all(r.station_id == "ST1" for r in refuels)
        assert h2_end >= 0

    def test_depleted_station_forces_next_best(self):
        route = self.long_route(8.0)
        # Empty station sits closest (at B) but has no stock; the planner must
        # skip it and take the reachable stocked one 4.4 km away.
        empty = Station(station_id="ST-EMPTY", name="Empty", lat=52.56, lon=13.40, available_kg=0.0)
        stocked = Station(station_id="ST-FULL", name="Full", lat=52.60, lon=13.40, available_kg=80.0)

        refuels, _, feasible, notes = plan_refuels(route, [empty, stocked], DEPOT)

        assert feasible, notes
        assert refuels and all(r.station_id == "ST-FULL" for r in refuels)
        assert empty.available_kg == 0.0  # never taken from an empty station

    def test_no_station_makes_long_route_infeasible(self):
        route = self.long_route(8.0)
        refuels, h2_end, feasible, notes = plan_refuels(route, [], DEPOT)
        assert not feasible
        assert notes, "an infeasible plan must explain itself"
        assert "no reachable station" in notes[0]

    def test_inventory_shared_across_buses(self):
        # Two buses refuel from one 20 kg station: the first drains it, the
        # second sees the depleted stock (inventory is not double-spent).
        station = Station(station_id="ST1", name="Hub", lat=52.56, lon=13.40, available_kg=20.0)
        r1 = self.long_route(8.0, bus_id="001")
        r2 = self.long_route(8.0, bus_id="002")

        refuels1, _, feasible1, _ = plan_refuels(r1, [station], DEPOT)
        after_first = station.available_kg
        refuels2, _, feasible2, notes2 = plan_refuels(r2, [station], DEPOT)

        assert feasible1 and refuels1
        taken1 = sum(r.kg_taken for r in refuels1)
        assert taken1 == pytest.approx(20.0)  # capped by stock, not by need
        assert after_first == pytest.approx(0.0)
        # Second bus cannot double-spend the drained inventory.
        assert not feasible2
        assert refuels2 == []
        assert notes2
        assert station.available_kg >= 0  # stock never goes negative
