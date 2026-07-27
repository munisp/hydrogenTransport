"""CO2-avoidance accounting core (shared by CLI batch and the read API).

Method: fleet distance per period is derived from odometer deltas in
fleet.telemetry (max - min per bus, robust to individual row loss), grouped
by vehicle energy_type (fleet.vehicles.energy_type, migration 0008; pre-0008
databases fall back to the legacy all-h2 aggregate). Baseline methodology is
per energy_type, credited against the diesel-reference baseline:

  h2      avoided = km * DIESEL_BASELINE_KG_CO2_PER_KM   (zero tailpipe)
  battery avoided = km * (diesel_baseline - EV_KWH_PER_KM * GRID_CO2_KG_PER_KWH)
          — diesel-reference credit minus the grid-electricity footprint of
          the energy consumed (documented, config-driven factors; floor 0)
  diesel  avoided = 0 — diesel IS the reference baseline (no credit)
  cng     avoided = km * (diesel_baseline - CNG_KG_CO2_PER_KM), floor 0

One credit = `credit_kg_co2` kg. The per-type breakdown is attached to the
issued event so the CARBON_FUND ledger-leg consumer (commerce-api) and other
downstreams can reconcile the issuance against the right baseline per bus
energy_type.

Idempotency (BUSINESS_LOGIC_AUDIT §14): citizen.carbon_credits has
UNIQUE(period) (migration 0005), and issuance is a single
INSERT ... ON CONFLICT (period) DO UPDATE, so concurrent computes for the
same period can never double-issue. The credit id is deterministic per
period (UUIDv5), so a recompute reissues the SAME credit identity and the
republished carbon.credit.issued event stays reconcilable with the row it
replaces instead of looking like a brand-new credit.
"""

from __future__ import annotations

import datetime as dt
import json
import logging
import re
import uuid
from dataclasses import dataclass, field

import asyncpg
from aiokafka import AIOKafkaProducer

from .config import settings

log = logging.getLogger("carbon-analytics")

SERVICE_NAME = "carbon-analytics"
_PERIOD_RE = re.compile(r"^\d{4}-(0[1-9]|1[0-2])$")

# UUIDv5 namespace for deterministic per-period credit ids (recompute-safe).
_CREDIT_NS = uuid.UUID("3f7a2c1e-9b4d-4e6a-8c5f-2d1b0a9e8f7c")


def credit_id_for_period(period: str) -> str:
    """Deterministic credit id for a period: a recompute reissues the same
    credit identity, so downstream consumers can reconcile the event with
    the replaced issuance instead of double-counting a new UUID."""
    return str(uuid.uuid5(_CREDIT_NS, f"carbon-credit:{period}"))


def baseline_kg_co2_avoided(energy_type: str, km: float,
                            cfg=settings) -> float:
    """Per-energy-type credit baseline (kg CO2 avoided for `km` driven).

    diesel-reference methodology: every type is credited against what a
    comparable diesel bus would have emitted for the same distance."""
    et = (energy_type or "h2").strip().lower()
    if et == "diesel":
        return 0.0  # diesel IS the reference baseline
    if et == "battery":
        electric = cfg.ev_kwh_per_km * cfg.grid_co2_kg_per_kwh
        return km * max(cfg.diesel_baseline_kg_co2_per_km - electric, 0.0)
    if et == "cng":
        return km * max(cfg.diesel_baseline_kg_co2_per_km - cfg.cng_kg_co2_per_km, 0.0)
    # h2 (and unknown/legacy types): zero tailpipe -> full diesel baseline.
    return km * cfg.diesel_baseline_kg_co2_per_km


_DISTANCE_SQL = """
SELECT coalesce(sum(km), 0)::float8 AS total_km, count(*)::int AS bus_count
FROM (
    SELECT bus_id, km FROM (
        SELECT bus_id, sum(d) AS km FROM (
            SELECT bus_id,
                   odometer_km - lag(odometer_km) OVER (PARTITION BY bus_id ORDER BY ts) AS d
            FROM fleet.telemetry
            WHERE ts >= $1 AND ts < $2
        ) deltas WHERE d > 0
        GROUP BY bus_id
    ) positive
    FROM fleet.telemetry
    WHERE ts >= $1 AND ts < $2
    GROUP BY bus_id
) d
"""

# Same distance math, grouped by vehicle energy_type (migration 0008).
_DISTANCE_BY_TYPE_SQL = """
SELECT coalesce(v.energy_type, 'h2') AS energy_type,
       coalesce(sum(d.km), 0)::float8 AS total_km, count(*)::int AS bus_count
FROM (
    SELECT bus_id, km FROM (
        SELECT bus_id, sum(d) AS km FROM (
            SELECT bus_id,
                   odometer_km - lag(odometer_km) OVER (PARTITION BY bus_id ORDER BY ts) AS d
            FROM fleet.telemetry
            WHERE ts >= $1 AND ts < $2
        ) deltas WHERE d > 0
        GROUP BY bus_id
    ) positive
    FROM fleet.telemetry
    WHERE ts >= $1 AND ts < $2
    GROUP BY bus_id
) d
JOIN fleet.vehicles v ON v.id = d.bus_id
GROUP BY 1
"""


async def _distance_by_energy_type(pool, start: dt.datetime, end: dt.datetime) -> list[tuple[str, float, int]]:
    """[(energy_type, total_km, bus_count)]. Falls back to the legacy all-h2
    aggregate on pre-0008 databases (no fleet.vehicles.energy_type column)."""
    try:
        rows = await pool.fetch(_DISTANCE_BY_TYPE_SQL, start, end)
        return [(str(r["energy_type"]), float(r["total_km"]), int(r["bus_count"]))
                for r in rows]
    except asyncpg.exceptions.UndefinedColumnError:
        log.info("fleet.vehicles.energy_type not present (pre-0008); "
                 "falling back to all-h2 baseline")
        row = await pool.fetchrow(_DISTANCE_SQL, start, end)
        return [("h2", float(row["total_km"]), int(row["bus_count"]))]


@dataclass
class PeriodResult:
    period: str
    total_km: float
    bus_count: int
    kg_co2_avoided: float
    credits: float
    credit_id: str = field(default="")
    event_published: bool = False
    # energy_type -> {total_km, bus_count, kg_co2_avoided} (per-type baseline
    # breakdown; attached to the issued event for downstream reconciliation).
    baseline_by_energy_type: dict = field(default_factory=dict)


def period_bounds(period: str) -> tuple[dt.datetime, dt.datetime]:
    """'2025-01' -> (2025-01-01Z, 2025-02-01Z)."""
    if not _PERIOD_RE.match(period):
        raise ValueError(f"period must be YYYY-MM, got {period!r}")
    year, month = int(period[:4]), int(period[5:7])
    start = dt.datetime(year, month, 1, tzinfo=dt.timezone.utc)
    end = (
        dt.datetime(year + 1, 1, 1, tzinfo=dt.timezone.utc)
        if month == 12
        else dt.datetime(year, month + 1, 1, tzinfo=dt.timezone.utc)
    )
    return start, end


def build_envelope(result: PeriodResult) -> bytes:
    now = dt.datetime.now(dt.timezone.utc)
    return json.dumps(
        {
            "id": str(uuid.uuid4()),
            "type": settings.output_topic,
            "source": SERVICE_NAME,
            "time": now.isoformat().replace("+00:00", "Z"),
            "data": {
                "credit_id": result.credit_id,
                "period": result.period,
                "kg_co2_avoided": result.kg_co2_avoided,
                "credits": result.credits,
                "baseline_by_energy_type": result.baseline_by_energy_type,
                "issued_at": now.isoformat().replace("+00:00", "Z"),
            },
        }
    ).encode()


async def compute_period(pool, period: str, publish: bool = True) -> PeriodResult:
    """Compute + persist + publish carbon credit for one YYYY-MM period."""
    start, end = period_bounds(period)
    by_type = await _distance_by_energy_type(pool, start, end)
    total_km = sum(km for _, km, _ in by_type)
    bus_count = sum(n for _, _, n in by_type)
    breakdown: dict[str, dict] = {}
    kg_avoided = 0.0
    for energy_type, km, n in by_type:
        kg = baseline_kg_co2_avoided(energy_type, km)
        kg_avoided += kg
        breakdown[energy_type] = {
            "total_km": round(km, 3),
            "bus_count": n,
            "kg_co2_avoided": round(kg, 3),
        }
    kg_avoided = round(kg_avoided, 3)
    credits = round(kg_avoided / settings.credit_kg_co2, 6)

    result = PeriodResult(
        period=period,
        total_km=round(total_km, 3),
        bus_count=bus_count,
        kg_co2_avoided=kg_avoided,
        credits=credits,
        baseline_by_energy_type=breakdown,
    )

    credit_id = credit_id_for_period(period)
    async with pool.acquire() as conn, conn.transaction():
        # Idempotent per period, race-safe: the UNIQUE(period) index
        # (migration 0005) serializes concurrent computes; the loser of a
        # race updates the winner's row instead of double-issuing.
        await conn.execute(
            """
            INSERT INTO citizen.carbon_credits (id, period, kg_co2_avoided, credits, issued_at)
            VALUES ($1::uuid, $2, $3, $4, now())
            ON CONFLICT (period) DO UPDATE SET
                kg_co2_avoided = EXCLUDED.kg_co2_avoided,
                credits        = EXCLUDED.credits,
                issued_at      = now()
            """,
            credit_id,
            period,
            kg_avoided,
            credits,
        )
    result.credit_id = credit_id

    if publish:
        producer = AIOKafkaProducer(bootstrap_servers=settings.kafka_brokers, acks="all")
        await producer.start()
        try:
            await producer.send_and_wait(
                settings.output_topic, build_envelope(result), key=period.encode()
            )
            result.event_published = True
        finally:
            await producer.stop()

    log.info(
        "period=%s km=%.1f buses=%d kg_co2_avoided=%.1f credits=%.4f published=%s",
        period, total_km, bus_count, kg_avoided, credits, result.event_published,
    )
    return result
