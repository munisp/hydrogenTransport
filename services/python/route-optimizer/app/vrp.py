"""OR-Tools VRP core with energy-range constraints + deterministic refuel planner.

Phase 1 (CP-SAT routing): assign/order daily stops across buses minimizing
total distance, with a per-vehicle distance dimension capped at each bus's
current energy range (units on board / consumption). Vehicles start at their
actual positions and end at the depot; stops that cannot fit any range budget
are dropped with a penalty and reported.

Phase 2 (deterministic refuel insertion): walk each route; whenever the next
leg would breach the safety range, insert the nearest reachable COMPATIBLE
station (station_type matches the bus energy_type; 'mixed' serves all but is
weighted by MIXED_STATION_DETOUR_FACTOR vs an exact-type station) with
sufficient inventory (inventory is decremented, so concurrent refuels respect
station stock).

Consumption is per energy_type (kg/100km h2+cng, kWh/100km battery,
L/100km diesel); the learned per-bus rate (fleet.fuel_consumption, Wave-4)
wins over the fleet defaults. For an h2 bus with no learned rate the math is
identical to the pre-Wave-5 H2-only behaviour.
"""

from __future__ import annotations

import math
from dataclasses import dataclass, field

from ortools.constraint_solver import pywrapcp, routing_enums_pb2

from .config import settings
from .models import Bus, Problem, RefuelEvent, Station, Stop, station_serves


def haversine_km(lat1: float, lon1: float, lat2: float, lon2: float) -> float:
    r = 6371.0088
    p1, p2 = math.radians(lat1), math.radians(lat2)
    dp, dl = math.radians(lat2 - lat1), math.radians(lon2 - lon1)
    a = math.sin(dp / 2) ** 2 + math.cos(p1) * math.cos(p2) * math.sin(dl / 2) ** 2
    return 2 * r * math.asin(math.sqrt(a))


@dataclass
class RawRoute:
    bus: Bus
    stops: list[Stop] = field(default_factory=list)
    km: float = 0.0


def consumption_per_km(bus: Bus) -> float:
    """Consumption in the bus's energy unit per km: learned per-bus rate
    (fleet.fuel_consumption) when available, fleet default per energy_type
    otherwise."""
    if bus.consumption_per_100km and bus.consumption_per_100km > 0:
        return bus.consumption_per_100km / 100.0
    return {
        "h2": settings.h2_consumption_kg_per_km,
        "battery": settings.battery_consumption_kwh_per_km,
        "diesel": settings.diesel_consumption_l_per_km,
        "cng": settings.cng_consumption_kg_per_km,
    }.get(bus.energy_type, settings.h2_consumption_kg_per_km)


def range_km(bus: Bus) -> float:
    return bus.level_units / consumption_per_km(bus)


def solve_vrp(problem: Problem) -> tuple[list[RawRoute], list[str], str]:
    """Phase 1. Returns (routes, dropped_stop_ids, solver_status)."""
    buses, stops = problem.buses, problem.stops
    v = len(buses)

    # Nodes: 0=depot(end) | 1..v=bus start positions | v+1.. = stops
    points: list[tuple[float, float]] = [(problem.depot.lat, problem.depot.lon)]
    points += [(b.lat, b.lon) for b in buses]
    points += [(s.lat, s.lon) for s in stops]

    n = len(points)
    dist_m = [[0] * n for _ in range(n)]
    for i in range(n):
        for j in range(n):
            if i != j:
                dist_m[i][j] = int(haversine_km(*points[i], *points[j]) * 1000)

    starts = [1 + i for i in range(v)]
    ends = [0] * v
    manager = pywrapcp.RoutingIndexManager(n, v, starts, ends)
    routing = pywrapcp.RoutingModel(manager)

    def distance_callback(from_index: int, to_index: int) -> int:
        return dist_m[manager.IndexToNode(from_index)][manager.IndexToNode(to_index)]

    transit_idx = routing.RegisterTransitCallback(distance_callback)
    routing.SetArcCostEvaluatorOfAllVehicles(transit_idx)

    # H2 range constraint: cumulative route distance may not exceed each bus's
    # current range (meters). Refuels are inserted afterwards in phase 2; a
    # route that cannot fit even with refuels drops stops instead of violating
    # the safety range.
    capacities = [int((range_km(b) + _max_refill_km(problem)) * 1000) for b in buses]
    routing.AddDimensionWithVehicleCapacity(transit_idx, 0, capacities, True, "Range")
    # Balance workload across the fleet (span of the Range dimension) while
    # still minimizing total distance.
    routing.GetDimensionOrDie("Range").SetGlobalSpanCostCoefficient(10)

    for i in range(len(stops)):
        node = manager.NodeToIndex(v + 1 + i)
        routing.AddDisjunction([node], settings.stop_drop_penalty * 1000)

    params = pywrapcp.DefaultRoutingSearchParameters()
    params.first_solution_strategy = routing_enums_pb2.FirstSolutionStrategy.PATH_CHEAPEST_ARC
    params.local_search_metaheuristic = routing_enums_pb2.LocalSearchMetaheuristic.GUIDED_LOCAL_SEARCH
    params.time_limit.FromSeconds(settings.solver_time_limit_s)

    assignment = routing.SolveWithParameters(params)
    status = routing.status()
    # Stable numbering across OR-Tools 9.x (RoutingSearchStatus.Value).
    status_name = {
        0: "NOT_SOLVED",
        1: "SUCCESS",
        2: "PARTIAL_SUCCESS",
        3: "FAIL",
        4: "FAIL_TIMEOUT",
        5: "INVALID",
        6: "INFEASIBLE",
        7: "OPTIMAL",
    }.get(status, str(status))

    routes = [RawRoute(bus=b) for b in buses]
    dropped: list[str] = []
    if assignment is None:
        # No solution at all: report every stop as unassigned.
        return routes, [s.stop_id for s in stops], status_name

    for vehicle in range(v):
        index = routing.Start(vehicle)
        prev_point = (buses[vehicle].lat, buses[vehicle].lon)
        while not routing.IsEnd(index):
            node = manager.IndexToNode(index)
            if node > v:  # stop node (start nodes are 1..v, depot 0)
                stop = stops[node - (v + 1)]
                routes[vehicle].stops.append(stop)
                routes[vehicle].km += haversine_km(*prev_point, stop.lat, stop.lon)
                prev_point = (stop.lat, stop.lon)
            index = assignment.Value(routing.NextVar(index))
        routes[vehicle].km += haversine_km(*prev_point, problem.depot.lat, problem.depot.lon)

    for i in range(len(stops)):
        node = manager.NodeToIndex(v + 1 + i)
        if assignment.Value(routing.VehicleVar(node)) == -1:
            dropped.append(stops[i].stop_id)

    return routes, dropped, status_name


def _max_refill_km(problem: Problem) -> float:
    """Headroom added to the solver capacity to allow one full refill en route
    (only when stations exist with stock)."""
    if not problem.stations:
        return 0.0
    return max((b.capacity_units / consumption_per_km(b) for b in problem.buses),
               default=0.0)


def _station_stock(st: Station, energy_type: str) -> float:
    """Stock relevant to the consuming energy_type: battery draws kWh
    (available_kwh column, 0008); everything else draws available_kg
    (litres for diesel stations — same numeric stock column)."""
    if energy_type == "battery" and st.available_kwh is not None:
        return st.available_kwh
    return st.available_kg


def _station_take(st: Station, energy_type: str, units: float) -> None:
    """Decrement stock in the unit the bus consumed (see _station_stock)."""
    if energy_type == "battery" and st.available_kwh is not None:
        st.available_kwh -= units
    else:
        st.available_kg -= units


def _station_weight(st: Station, energy_type: str) -> float:
    """Exact-type stations are preferred; 'mixed' serves all but is weighted
    by MIXED_STATION_DETOUR_FACTOR (a mixed station must be meaningfully
    closer to beat a dedicated one)."""
    if st.station_type == "mixed":
        return settings.mixed_station_detour_factor
    return 1.0


def plan_refuels(route: RawRoute, stations: list[Station], depot: Stop) -> tuple[list[RefuelEvent], float, bool, list[str]]:
    """Phase 2. Returns (refuels, energy_end, feasible, notes) where
    energy_end is in the bus's energy unit. Decrements stock on the shared
    Station objects so concurrent refuels across buses respect station stock.
    Only stations whose station_type serves the bus's energy_type are
    considered ('mixed' serves all, weighted by the detour factor)."""
    bus = route.bus
    consumption = consumption_per_km(bus)
    safety = settings.range_safety_km
    notes: list[str] = []
    refuels: list[RefuelEvent] = []

    remaining_km = range_km(bus)
    cur = (bus.lat, bus.lon)
    waypoints: list[tuple[str, float, float]] = [(s.stop_id, s.lat, s.lon) for s in route.stops]
    waypoints.append((depot.stop_id, depot.lat, depot.lon))  # return to depot

    for seq, (stop_id, lat, lon) in enumerate(waypoints):
        leg = haversine_km(*cur, lat, lon)
        if remaining_km - leg < safety:
            # Need energy before this leg: pick the nearest reachable
            # compatible station with enough stock to make the stop worthwhile.
            needed_units = bus.capacity_units - remaining_km * consumption
            candidates = []
            for st in stations:
                if not station_serves(st.station_type, bus.energy_type):
                    continue  # incompatible station_type for this energy_type
                d_to = haversine_km(*cur, st.lat, st.lon)
                if d_to > remaining_km - safety:
                    continue  # cannot reach safely
                if _station_stock(st, bus.energy_type) < min(needed_units, 5.0):
                    continue  # not enough stock
                candidates.append((d_to * _station_weight(st, bus.energy_type), d_to, st))
            if not candidates:
                notes.append(
                    f"no reachable station with stock before {stop_id} "
                    f"(compatible with {bus.energy_type}; remaining {remaining_km:.1f} km)"
                )
                return refuels, remaining_km * consumption, False, notes
            _, d_to, st = min(candidates, key=lambda c: (c[0], c[1]))
            units_taken = min(needed_units, _station_stock(st, bus.energy_type))
            _station_take(st, bus.energy_type, units_taken)
            refuels.append(
                RefuelEvent(
                    station_id=st.station_id,
                    station_name=st.name,
                    kg_taken=round(units_taken, 2),
                    at_stop_sequence=seq,
                    remaining_range_km_before=round(remaining_km, 1),
                    station_type=st.station_type,
                    energy_unit=bus.energy_unit,
                )
            )
            remaining_km = (remaining_km - d_to) + units_taken / consumption
            cur = (st.lat, st.lon)
            leg = haversine_km(*cur, lat, lon)
            if remaining_km - leg < safety:
                notes.append(f"range still insufficient for {stop_id} after refuel at {st.name}")
                return refuels, remaining_km * consumption, False, notes
        remaining_km -= leg
        cur = (lat, lon)

    return refuels, remaining_km * consumption, True, notes
