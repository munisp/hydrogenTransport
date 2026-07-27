"""Batch CLI: compute CO2 credits for a period (or the previous month).

Usage:
    python -m app.cli --period 2025-01
    python -m app.cli                 # previous calendar month
    python -m app.cli --no-publish    # DB write only, no Kafka event

Gated on the `carbon-credits` toggle: exits cleanly (code 0) when disabled.
"""

from __future__ import annotations

import argparse
import asyncio
import datetime as dt
import json
import logging
import sys

import asyncpg
from toggle_client import AsyncToggleClient

from .config import settings
from .core import compute_period

logging.basicConfig(level=logging.INFO, format="%(asctime)s %(levelname)s %(name)s %(message)s")
log = logging.getLogger("carbon-analytics.cli")


def previous_month(today: dt.date | None = None) -> str:
    today = today or dt.date.today()
    year, month = (today.year - 1, 12) if today.month == 1 else (today.year, today.month - 1)
    return f"{year:04d}-{month:02d}"


async def amain() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--period", default=None, help="YYYY-MM (default: previous month)")
    parser.add_argument("--no-publish", action="store_true", help="skip Kafka publication")
    args = parser.parse_args()
    period = args.period or previous_month()

    toggles = AsyncToggleClient(settings.toggle_url)
    try:
        if not await toggles.is_enabled(settings.toggle_module):
            log.info("module %s is disabled; nothing to do", settings.toggle_module)
            return 0
    finally:
        await toggles.close()

    pool = await asyncpg.create_pool(settings.database_url, min_size=1, max_size=2)
    try:
        result = await compute_period(pool, period, publish=not args.no_publish)
    finally:
        await pool.close()

    print(
        json.dumps(
            {
                "period": result.period,
                "total_km": result.total_km,
                "bus_count": result.bus_count,
                "kg_co2_avoided": result.kg_co2_avoided,
                "credits": result.credits,
                "credit_id": result.credit_id,
                "event_published": result.event_published,
                "baseline_by_energy_type": result.baseline_by_energy_type,
            },
            indent=2,
        )
    )
    return 0


def main() -> None:
    sys.exit(asyncio.run(amain()))


if __name__ == "__main__":
    main()
