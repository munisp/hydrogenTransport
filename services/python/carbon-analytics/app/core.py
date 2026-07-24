"""CO2-avoidance accounting core (shared by CLI batch and the read API).

Method: fleet distance per period is derived from odometer deltas in
fleet.telemetry (max - min per bus, robust to individual row loss). Diesel
baseline factor: 1.2 kg CO2 / km (SPEC/mission). H2 fleet tailpipe emissions
are zero, so avoided = distance * baseline. One credit = `credit_kg_co2` kg.
Writes are idempotent per period (delete + insert in one transaction).
"""

from __future__ import annotations

import datetime as dt
import json
import logging
import re
import uuid
from dataclasses import dataclass, field

from aiokafka import AIOKafkaProducer

from .config import settings

log = logging.getLogger("carbon-analytics")

SERVICE_NAME = "carbon-analytics"
_PERIOD_RE = re.compile(r"^\d{4}-(0[1-9]|1[0-2])$")

_DISTANCE_SQL = """
SELECT coalesce(sum(km), 0)::float8 AS total_km, count(*)::int AS bus_count
FROM (
    SELECT bus_id, max(odometer_km) - min(odometer_km) AS km
    FROM fleet.telemetry
    WHERE ts >= $1 AND ts < $2
    GROUP BY bus_id
) d
"""


@dataclass
class PeriodResult:
    period: str
    total_km: float
    bus_count: int
    kg_co2_avoided: float
    credits: float
    credit_id: str = field(default="")
    event_published: bool = False


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
                "issued_at": now.isoformat().replace("+00:00", "Z"),
            },
        }
    ).encode()


async def compute_period(pool, period: str, publish: bool = True) -> PeriodResult:
    """Compute + persist + publish carbon credit for one YYYY-MM period."""
    start, end = period_bounds(period)
    row = await pool.fetchrow(_DISTANCE_SQL, start, end)
    total_km = float(row["total_km"])
    bus_count = int(row["bus_count"])
    kg_avoided = round(total_km * settings.diesel_baseline_kg_co2_per_km, 3)
    credits = round(kg_avoided / settings.credit_kg_co2, 6)

    result = PeriodResult(
        period=period,
        total_km=round(total_km, 3),
        bus_count=bus_count,
        kg_co2_avoided=kg_avoided,
        credits=credits,
    )

    credit_id = str(uuid.uuid4())
    async with pool.acquire() as conn, conn.transaction():
        # Idempotent per period: recompute replaces the previous issuance.
        await conn.execute("DELETE FROM citizen.carbon_credits WHERE period = $1", period)
        await conn.execute(
            """
            INSERT INTO citizen.carbon_credits (id, period, kg_co2_avoided, credits, issued_at)
            VALUES ($1::uuid, $2, $3, $4, now())
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
