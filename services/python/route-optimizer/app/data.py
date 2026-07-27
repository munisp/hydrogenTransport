"""Problem data loading: Postgres (fleet.vehicles + latest telemetry +
infra.stations + fleet.route_stops) with deterministic fallbacks when the
DB has no fleet yet (SPEC §4: simulated fallbacks allowed).

Route stops come from fleet.route_stops / fleet.stops (migration 0005) when
populated; on databases without those tables (or with an empty network) the
legacy deterministic per-date generator is used instead, so the VRP input
contract (`list[Stop]`) never changes.
"""

from __future__ import annotations

import datetime as dt
import hashlib
import logging
import random

import asyncpg

from .models import Bus, Problem, Station, Stop

log = logging.getLogger("route-optimizer")

# Berlin-ish operating area for the seed fleet.
_SEED_CENTER = (52.5200, 13.4050)


async def load_problem(pool, bus_ids: list[str] | None, date: dt.date) -> tuple[Problem, str]:
    buses = await _load_buses(pool, bus_ids)
    stations = await _load_stations(pool)
    if not buses:
        return seed_problem(bus_ids, date), "seed"
    stops = await load_stops(pool, date)
    depot = Stop(stop_id="DEPOT-CENTRAL", name="Central Depot", lat=_SEED_CENTER[0], lon=_SEED_CENTER[1])
    return Problem(depot=depot, buses=buses, stations=stations, stops=stops), "database"


async def load_stops(pool, date: dt.date) -> list[Stop]:
    """Scheduled route stops from fleet.route_stops / fleet.stops (0005),
    ordered by route/sequence. Falls back to the deterministic per-date
    generator when the tables are missing (pre-0005 database) or empty."""
    try:
        rows = await pool.fetch(
            """
            SELECT s.code AS stop_id, s.name,
                   ST_Y(s.geom)::float8 AS lat, ST_X(s.geom)::float8 AS lon
            FROM fleet.route_stops rs
            JOIN fleet.stops s ON s.id = rs.stop_id
            ORDER BY rs.route_id, rs.seq
            """
        )
    except asyncpg.exceptions.UndefinedTableError:
        log.info("fleet.route_stops/fleet.stops not present; using generated stops")
        rows = []
    if not rows:
        return generate_stops(date)
    return [Stop(**dict(r)) for r in rows]


_BUSES_SQL = """
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

# Wave-5 variant: energy_type (0008) + learned per-bus consumption
# (fleet.fuel_consumption, Wave-4; fleet-api GET /v1/fuel/levels exposes the
# same rate). energy_level_pct (0008) is preferred for non-h2 buses.
_BUSES_SQL_ENERGY = """
        SELECT v.id::text                                   AS bus_id,
               v.fleet_no,
               coalesce(t.lat, ST_Y(v.geom))                AS lat,
               coalesce(t.lon, ST_X(v.geom))                AS lon,
               v.h2_capacity_kg::float8,
               coalesce(t.energy_level_pct, t.h2_level_pct, 100)::float8 AS h2_level_pct,
               coalesce(v.energy_type, 'h2')                AS energy_type,
               fc.kg_per_100km::float8                      AS learned_per_100km
        FROM fleet.vehicles v
        LEFT JOIN LATERAL (
            SELECT h2_level_pct, energy_level_pct,
                   ST_Y(tel.geom) AS lat, ST_X(tel.geom) AS lon
            FROM fleet.telemetry tel
            WHERE tel.bus_id = v.id
            ORDER BY tel.ts DESC
            LIMIT 1
        ) t ON true
        LEFT JOIN fleet.fuel_consumption fc ON fc.bus_id = v.id
        WHERE v.status = 'active'
    """

_STATIONS_SQL = """
        SELECT id::text AS station_id, name, ST_Y(geom) AS lat, ST_X(geom) AS lon,
               available_kg::float8
        FROM infra.stations
        WHERE status = 'online' AND available_kg > 0
        """

# Wave-5 variant: station_type + kWh inventory (0008).
_STATIONS_SQL_ENERGY = """
        SELECT id::text AS station_id, name, ST_Y(geom) AS lat, ST_X(geom) AS lon,
               available_kg::float8,
               coalesce(station_type, 'h2') AS station_type,
               available_kwh::float8
        FROM infra.stations
        WHERE status = 'online'
          AND (available_kg > 0 OR available_kwh > 0)
        """


async def _load_buses(pool, bus_ids: list[str] | None) -> list[Bus]:
    args: list = []
    clause = ""
    if bus_ids:
        clause = " AND v.id = ANY($1::uuid[])"
        args.append(bus_ids)
    try:
        rows = await pool.fetch(_BUSES_SQL_ENERGY + clause, *args)
    except (asyncpg.exceptions.UndefinedColumnError,
            asyncpg.exceptions.UndefinedTableError):
        log.info("energy_type/fuel_consumption not present (pre-0008/0007); "
                 "using legacy H2 bus query")
        rows = await pool.fetch(_BUSES_SQL + clause, *args)
    buses = []
    for r in rows:
        d = dict(r)
        learned = d.pop("learned_per_100km", None)
        bus = Bus(**d)
        if learned and learned > 0:
            bus.consumption_per_100km = float(learned)
            bus.consumption_source = "learned"
        buses.append(bus)
    return buses


async def _load_stations(pool) -> list[Station]:
    try:
        rows = await pool.fetch(_STATIONS_SQL_ENERGY)
    except asyncpg.exceptions.UndefinedColumnError:
        log.info("infra.stations station_type/available_kwh not present "
                 "(pre-0008); using legacy H2 station query")
        rows = await pool.fetch(_STATIONS_SQL)
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


async def write_back_inventory(pool, plans) -> None:
    """Atomically apply the planned refuel draw-down to infra.stations.
    Refuels whose recorded stock no longer covers the planned amount are
    skipped and annotated on the plan (never negative stock). kWh refuels
    draw from available_kwh (migration 0008), everything else from
    available_kg."""
    async with pool.acquire() as conn:
        # Probe once (outside the tx body a failed UPDATE would poison it).
        has_kwh = bool(await conn.fetchval(
            "SELECT EXISTS (SELECT 1 FROM information_schema.columns "
            "WHERE table_schema = 'infra' AND table_name = 'stations' "
            "AND column_name = 'available_kwh')"))
        async with conn.transaction():
            for plan in plans:
                for refuel in plan.refuels:
                    if refuel.energy_unit == "kwh":
                        if not has_kwh:
                            plan.notes.append(
                                f"inventory write-back skipped for station {refuel.station_id}: "
                                "available_kwh not present (pre-0008 schema)")
                            continue
                        result = await conn.execute(
                            """
                            UPDATE infra.stations SET available_kwh = available_kwh - $2
                            WHERE station_id = $1 AND available_kwh >= $2
                            """,
                            refuel.station_id, refuel.kg_taken,
                        )
                    else:
                        result = await conn.execute(
                            """
                            UPDATE infra.stations SET available_kg = available_kg - $2
                            WHERE station_id = $1 AND available_kg >= $2
                            """,
                            refuel.station_id, refuel.kg_taken,
                        )
                    if result.endswith(" 0"):
                        plan.notes.append(
                            f"inventory write-back skipped for station {refuel.station_id}: "
                            "insufficient recorded stock")
