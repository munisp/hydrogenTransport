"""OR-Tools VRP core with H2 range constraints + deterministic refuel planner.

Phase 1 (CP-SAT routing): assign/order daily stops across buses minimizing
total distance, with a per-vehicle distance dimension capped at each bus's
current H2 range (kg on board / consumption). Vehicles start at their actual
positions and end at the depot; stops that cannot fit any range budget are
dropped with a penalty and reported.

Phase 2 (deterministic refuel insertion): walk each route; whenever the next
leg would breach the safety range, insert the nearest reachable station with
sufficient inventory (inventory is decremented, so concurrent refuels respect
station stock).
"""

from __future__ import annotations

import math
from dataclasses import dataclass, field

from ortools.constraint_solver import pywrapcp, routing_enums_pb2

from .config import settings
from .models import Bus, Problem, RefuelEvent, Station, Stop


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


def range_km(bus: Bus) -> float:
    return bus.h2_kg / settings.h2_consumption_kg_per_km


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
    max_capacity = max((b.h2_capacity_kg for b in problem.buses), default=0.0)
    return max_capacity / settings.h2_consumption_kg_per_km


def plan_refuels(route: RawRoute, stations: list[Station], depot: Stop) -> tuple[list[RefuelEvent], float, bool, list[str]]:
    """Phase 2. Returns (refuels, h2_end_kg, feasible, notes). Decrements
    `available_kg` on the shared Station objects so concurrent refuels across
    buses respect station stock."""
    bus = route.bus
    consumption = settings.h2_consumption_kg_per_km
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
            # Need hydrogen before this leg: pick nearest reachable station
            # with enough stock to make the stop worthwhile.
            needed_kg = bus.h2_capacity_kg - remaining_km * consumption
            candidates = []
            for st in stations:
                d_to = haversine_km(*cur, st.lat, st.lon)
                if d_to > remaining_km - safety:
                    continue  # cannot reach safely
                if st.available_kg < min(needed_kg, 5.0):
                    continue  # not enough stock
                candidates.append((d_to, st))
            if not candidates:
                notes.append(
                    f"no reachable station with stock before {stop_id} "
                    f"(remaining {remaining_km:.1f} km)"
                )
                return refuels, remaining_km * consumption, False, notes
            d_to, st = min(candidates, key=lambda c: c[0])
            kg_taken = min(needed_kg, st.available_kg)
            st.available_kg -= kg_taken
            refuels.append(
                RefuelEvent(
                    station_id=st.station_id,
                    station_name=st.name,
                    kg_taken=round(kg_taken, 2),
                    at_stop_sequence=seq,
                    remaining_range_km_before=round(remaining_km, 1),
                )
            )
            remaining_km = (remaining_km - d_to) + kg_taken / consumption
            cur = (st.lat, st.lon)
            leg = haversine_km(*cur, lat, lon)
            if remaining_km - leg < safety:
                notes.append(f"range still insufficient for {stop_id} after refuel at {st.name}")
                return refuels, remaining_km * consumption, False, notes
        remaining_km -= leg
        cur = (lat, lon)

    return refuels, remaining_km * consumption, True, notes
