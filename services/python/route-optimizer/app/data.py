"""Problem data loading: Postgres (fleet.vehicles + latest telemetry +
infra.stations) with a deterministic seed-data fallback when the DB has no
fleet yet (SPEC §4: simulated fallbacks allowed).

Scheduled route stops are not yet modeled in SPEC §3.4, so per-day waypoints
come from a deterministic generator (seeded by date); swap `generate_stops`
for a route_stops table lookup when infra/sql adds one — the VRP input
contract (`list[Stop]`) does not change.
"""

from __future__ import annotations

import datetime as dt
import hashlib
import random

from .models import Bus, Problem, Station, Stop

# Berlin-ish operating area for the seed fleet.
_SEED_CENTER = (52.5200, 13.4050)


async def load_problem(pool, bus_ids: list[str] | None, date: dt.date) -> tuple[Problem, str]:
    buses = await _load_buses(pool, bus_ids)
    stations = await _load_stations(pool)
    if not buses:
        return seed_problem(bus_ids, date), "seed"
    stops = generate_stops(date)
    depot = Stop(stop_id="DEPOT-CENTRAL", name="Central Depot", lat=_SEED_CENTER[0], lon=_SEED_CENTER[1])
    return Problem(depot=depot, buses=buses, stations=stations, stops=stops), "database"


async def _load_buses(pool, bus_ids: list[str] | None) -> list[Bus]:
    sql = """
        SELECT v.id::text                                   AS bus_id,
               v.fleet_no,
               coalesce(t.lat, ST_Y(v.geom))                AS lat,
               coalesce(t.lon, ST_X(v.geom))                AS lon,
               v.h2_capacity_kg::float8,
               coalesce(t.h2_level_pct, 100)::float8       AS h2_level_pct
        FROM fleet.vehicles v
        LEFT JOIN LATERAL (
            SELECT h2_level_pct, ST_Y(tel.geom) AS lat, ST_X(tel.geom) AS lon
            FROM fleet.telemetry tel
            WHERE tel.bus_id = v.id
            ORDER BY tel.ts DESC
            LIMIT 1
        ) t ON true
        WHERE v.status = 'active'
    """
    args: list = []
    if bus_ids:
        sql += " AND v.id = ANY($1::uuid[])"
        args.append(bus_ids)
    rows = await pool.fetch(sql, *args)
    return [Bus(**dict(r)) for r in rows]


async def _load_stations(pool) -> list[Station]:
    rows = await pool.fetch(
        """
        SELECT id::text AS station_id, name, ST_Y(geom) AS lat, ST_X(geom) AS lon,
               available_kg::float8
        FROM infra.stations
        WHERE status = 'online' AND available_kg > 0
        """
    )
    return [Station(**dict(r)) for r in rows]


def generate_stops(date: dt.date, n: int = 12) -> list[Stop]:
    """Deterministic daily route-stop set (seeded by date)."""
    rng = random.Random(f"h2fleet-stops-{date.isoformat()}")
    lat0, lon0 = _SEED_CENTER
    stops = []
    for i in range(n):
        stops.append(
            Stop(
                stop_id=f"STOP-{i + 1:02d}",
                name=f"Route stop {i + 1}",
                lat=lat0 + rng.uniform(-0.06, 0.06),
                lon=lon0 + rng.uniform(-0.10, 0.10),
            )
        )
    return stops


def seed_problem(bus_ids: list[str] | None, date: dt.date) -> Problem:
    """Fully deterministic demo fleet: 5 buses, 3 stations, 12 stops."""
    rng = random.Random(f"h2fleet-seed-{date.isoformat()}")
    lat0, lon0 = _SEED_CENTER
    fleet = [f"H2-{i + 1:03d}" for i in range(5)]
    if bus_ids:
        wanted = set(bus_ids)
        fleet = [f for f in fleet if f in wanted] or fleet

    buses = []
    for fleet_no in fleet:
        digest = hashlib.sha256(fleet_no.encode()).hexdigest()
        buses.append(
            Bus(
                bus_id=f"{digest[0:8]}-{digest[8:12]}-{digest[12:16]}-{digest[16:20]}-{digest[20:32]}",
                fleet_no=fleet_no,
                lat=lat0 + rng.uniform(-0.02, 0.02),
                lon=lon0 + rng.uniform(-0.02, 0.02),
                h2_capacity_kg=37.5,
                h2_level_pct=round(rng.uniform(35.0, 95.0), 1),
            )
        )
    stations = [
        Station(station_id="ST-NORTH", name="North H2 Station", lat=lat0 + 0.05, lon=lon0 - 0.02, available_kg=400.0),
        Station(station_id="ST-EAST", name="East H2 Station", lat=lat0 - 0.01, lon=lon0 + 0.08, available_kg=250.0),
        Station(station_id="ST-SOUTH", name="South H2 Station", lat=lat0 - 0.055, lon=lon0 + 0.01, available_kg=500.0),
    ]
    depot = Stop(stop_id="DEPOT-CENTRAL", name="Central Depot", lat=lat0, lon=lon0)
    return Problem(depot=depot, buses=buses, stations=stations, stops=generate_stops(date))
