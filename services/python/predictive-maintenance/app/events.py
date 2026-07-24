"""Kafka loop: consumes telemetry.enriched to track active buses, periodically
scores them, persists fleet.maintenance_predictions and publishes
maintenance.predicted for high-risk components. Gated on the
predictive-maintenance toggle (idle when disabled)."""

from __future__ import annotations

import asyncio
import json
import logging
import uuid
from datetime import datetime, timedelta, timezone

from aiokafka import AIOKafkaConsumer, AIOKafkaProducer

from .config import settings
from .features import fetch_features
from .model import ComponentRisk

log = logging.getLogger("predictive-maintenance.events")

SERVICE_NAME = "predictive-maintenance"


def build_envelope(data: dict) -> bytes:
    return json.dumps(
        {
            "id": str(uuid.uuid4()),
            "type": settings.output_topic,
            "source": SERVICE_NAME,
            "time": datetime.now(timezone.utc).isoformat().replace("+00:00", "Z"),
            "data": data,
        }
    ).encode()


async def persist_predictions(pool, bus_id: str, model_version: str, risks: list[ComponentRisk]) -> None:
    now = datetime.now(timezone.utc)
    rows = [
        (
            str(uuid.uuid4()),
            bus_id,
            r.component,
            r.risk_score,
            now + timedelta(days=r.horizon_days),
            model_version,
            now,
        )
        for r in risks
    ]
    await pool.executemany(
        """
        INSERT INTO fleet.maintenance_predictions
            (id, bus_id, component, risk_score, predicted_failure_at, model_version, created_at)
        VALUES ($1::uuid, $2::uuid, $3, $4, $5, $6, $7)
        """,
        rows,
    )


async def score_bus(pool, producer, bus_id: str, model) -> list[ComponentRisk]:
    features = await fetch_features(pool, bus_id, settings.feature_window_hours)
    if features is None:
        return []
    risks = model.predict_all(features)
    await persist_predictions(pool, bus_id, model.version, risks)
    for r in risks:
        if r.risk_score >= settings.high_risk_threshold:
            payload = build_envelope(
                {
                    "bus_id": bus_id,
                    "component": r.component,
                    "risk_score": r.risk_score,
                    "predicted_failure_at": (
                        datetime.now(timezone.utc) + timedelta(days=r.horizon_days)
                    ).isoformat().replace("+00:00", "Z"),
                    "model_version": model.version,
                }
            )
            await producer.send_and_wait(settings.output_topic, payload, key=bus_id.encode())
            log.info("high-risk prediction published bus=%s component=%s risk=%.3f",
                     bus_id, r.component, r.risk_score)
    return risks


async def run_consumer_loop(pool, toggles, model_holder: dict) -> None:
    """Long-running task; cancelled on shutdown. `model_holder` is a mutable
    dict so a retrained model can be hot-swapped by the API process."""
    consumer = AIOKafkaConsumer(
        settings.input_topic,
        bootstrap_servers=settings.kafka_brokers,
        group_id=settings.kafka_group_id,
        enable_auto_commit=True,
        auto_offset_reset="latest",
    )
    producer = AIOKafkaProducer(bootstrap_servers=settings.kafka_brokers, acks="all")
    await consumer.start()
    await producer.start()
    log.info("kafka loop started: consume %s, publish %s", settings.input_topic, settings.output_topic)

    active_buses: dict[str, datetime] = {}
    last_scoring = datetime.min.replace(tzinfo=timezone.utc)

    try:
        while True:
            if not await toggles.is_enabled(settings.toggle_module):
                await asyncio.sleep(5)
                continue

            batch = await consumer.getmany(timeout_ms=1000)
            now = datetime.now(timezone.utc)
            for _tp, records in batch.items():
                for rec in records:
                    try:
                        env = json.loads(rec.value)
                        bus_id = env.get("data", {}).get("bus_id")
                        if bus_id:
                            active_buses[bus_id] = now
                    except Exception:
                        log.warning("dropping malformed telemetry.enriched message")

            # Evict buses not seen for 2x the scoring interval.
            cutoff = now - timedelta(seconds=2 * settings.scoring_interval_s)
            for bus_id in [b for b, ts in active_buses.items() if ts < cutoff]:
                del active_buses[bus_id]

            if now - last_scoring >= timedelta(seconds=settings.scoring_interval_s):
                last_scoring = now
                model = model_holder["model"]
                for bus_id in list(active_buses):
                    try:
                        await score_bus(pool, producer, bus_id, model)
                    except Exception:
                        log.exception("scoring failed for bus %s", bus_id)
    finally:
        await consumer.stop()
        await producer.stop()
