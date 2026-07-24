"""Fleet loader, Redis enrichment metadata and Kafka publisher loop.

Pipeline (SPEC §3.3 envelope, topic ``telemetry.raw``):

    fleet.vehicles (Postgres) ─▶ bus states
    HSET bus:meta:<id> {route_id, depot_id, heading_deg, fleet_no}  (Redis)
    every SIM_INTERVAL_SECONDS: one envelope per bus ─▶ Kafka telemetry.raw

Downstream (telemetry-ingest) joins the raw envelope with the Redis
``bus:meta:*`` hashes to produce ``telemetry.enriched``.
"""
from __future__ import annotations

import asyncio
import json
import logging
import uuid
from datetime import datetime, timezone

import asyncpg
import redis.asyncio as aioredis
from aiokafka import AIOKafkaProducer

from .config import config
from .state import BusState

log = logging.getLogger("telemetry-simulator")

ROUTES = ("R1", "R2", "R3", "R4", "R5", "R6")
FALLBACK_DEPOT = "depot-central"


def _rfc3339_now() -> str:
    return datetime.now(timezone.utc).isoformat(timespec="milliseconds").replace("+00:00", "Z")


async def _connect_with_retry():
    """Wait for Postgres/Redis/Kafka — the simulator may start before them."""
    last_err: Exception | None = None
    for attempt in range(1, config.CONNECT_RETRIES + 1):
        try:
            pg = await asyncpg.connect(config.DATABASE_URL.replace("?sslmode=disable", ""))
            host, port = config.redis_host_port()
            rds = aioredis.Redis(host=host, port=port, decode_responses=True)
            await rds.ping()
            producer = AIOKafkaProducer(
                bootstrap_servers=config.KAFKA_BROKERS.split(","),
                value_serializer=lambda v: json.dumps(v).encode("utf-8"),
                key_serializer=lambda k: k.encode("utf-8"),
                enable_idempotence=True,
            )
            await producer.start()
            return pg, rds, producer
        except Exception as err:  # noqa: BLE001 — retry on anything, log and wait
            last_err = err
            log.warning(
                "middleware not ready (attempt %d/%d): %s",
                attempt, config.CONNECT_RETRIES, err,
            )
            await asyncio.sleep(config.CONNECT_RETRY_SECONDS)
    raise RuntimeError(f"could not connect to middleware: {last_err}")


async def load_buses(pg: asyncpg.Connection) -> list[BusState]:
    rows = await pg.fetch(
        "SELECT id, fleet_no, status, ST_Y(geom) AS lat, ST_X(geom) AS lon "
        "FROM fleet.vehicles ORDER BY fleet_no"
    )
    if not rows:
        raise RuntimeError("fleet.vehicles is empty — run the SQL seed first")
    depots = [str(r["id"]) for r in await pg.fetch("SELECT id FROM infra.stations ORDER BY name")]
    if not depots:
        depots = [FALLBACK_DEPOT]
    buses: list[BusState] = []
    for i, r in enumerate(rows):
        buses.append(
            BusState(
                bus_id=str(r["id"]),
                fleet_no=r["fleet_no"],
                status=r["status"],
                lat=float(r["lat"]),
                lon=float(r["lon"]),
                route_id=ROUTES[i % len(ROUTES)],
                depot_id=depots[i % len(depots)],
            )
        )
    log.info("loaded %d buses (%d depots, %d routes)", len(buses), len(depots), len(ROUTES))
    return buses


async def publish_meta(rds: aioredis.Redis, buses: list[BusState]) -> None:
    """HSET bus:meta:<id> — enrichment lookup used by telemetry-ingest."""
    async with rds.pipeline(transaction=False) as pipe:
        for b in buses:
            await pipe.hset(
                f"bus:meta:{b.bus_id}",
                mapping={
                    "fleet_no": b.fleet_no,
                    "route_id": b.route_id,
                    "depot_id": b.depot_id,
                    "heading_deg": f"{b.heading_deg:.1f}",
                },
            )
        await pipe.execute()
    log.info("redis bus:meta:* hashes written for %d buses", len(buses))


def envelope(bus: BusState) -> dict:
    """CloudEvents-ish envelope per SPEC §3.3, data per telemetry.raw schema."""
    now = _rfc3339_now()
    return {
        "id": str(uuid.uuid4()),
        "type": config.KAFKA_TOPIC,
        "source": config.SIM_SOURCE,
        "time": now,
        "data": {
            "bus_id": bus.bus_id,
            "ts": now,
            "speed_kph": round(bus.speed_kph, 1),
            "h2_level_pct": round(bus.h2_level_pct, 2),
            "fuel_cell_kw": round(bus.fuel_cell_kw, 1),
            "battery_soc_pct": round(bus.battery_soc_pct, 1),
            "odometer_km": round(bus.odometer_km, 2),
            "lat": round(bus.lat, 6),
            "lon": round(bus.lon, 6),
            # Extra context (schema allows additionalProperties in data).
            "fleet_no": bus.fleet_no,
            "route_id": bus.route_id,
            "depot_id": bus.depot_id,
            "heading_deg": round(bus.heading_deg, 1),
            "status": bus.status,
        },
    }


async def run(stop: asyncio.Event) -> None:
    pg, rds, producer = await _connect_with_retry()
    heartbeat = "/tmp/sim-heartbeat"
    try:
        buses = await load_buses(pg)
        await publish_meta(rds, buses)
        interval = config.SIM_INTERVAL_SECONDS
        log.info("publishing %d envelopes every %.1fs to %s", len(buses), interval, config.KAFKA_TOPIC)
        while not stop.is_set():
            loop_start = asyncio.get_running_loop().time()
            refuels = 0
            for b in buses:
                if b.step(interval, config.SIM_H2_DRAIN_PCT_PER_KM, config.SIM_REFUEL_THRESHOLD_PCT):
                    refuels += 1
                env = envelope(b)
                await producer.send_and_wait(config.KAFKA_TOPIC, key=b.bus_id, value=env)
                # Keep Redis heading fresh for enrichment consumers.
                await rds.hset(f"bus:meta:{b.bus_id}", "heading_deg", f"{b.heading_deg:.1f}")
            # Heartbeat file drives the container healthcheck.
            with open(heartbeat, "w", encoding="utf-8") as fh:
                fh.write(_rfc3339_now())
            if refuels:
                log.info("%d bus(es) refuelled this tick", refuels)
            elapsed = asyncio.get_running_loop().time() - loop_start
            try:
                await asyncio.wait_for(stop.wait(), timeout=max(0.1, interval - elapsed))
            except asyncio.TimeoutError:
                pass
    finally:
        await producer.stop()
        await rds.aclose()
        await pg.close()
        log.info("simulator stopped cleanly")
