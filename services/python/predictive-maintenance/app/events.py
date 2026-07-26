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
from .features import fetch_features, fetch_sequence
from .model import ComponentRisk

log = logging.getLogger("predictive-maintenance.events")

SERVICE_NAME = "predictive-maintenance"


def build_envelope(data: dict, topic: str | None = None) -> bytes:
    return json.dumps(
        {
            "id": str(uuid.uuid4()),
            "type": topic or settings.output_topic,
            "source": SERVICE_NAME,
            "time": datetime.now(timezone.utc).isoformat().replace("+00:00", "Z"),
            "data": data,
        }
    ).encode()


async def persist_predictions(pool, bus_id: str, model_version: str, risks: list[ComponentRisk]) -> dict[str, str]:
    """Persists the predictions; returns component -> prediction id so the
    maintenance.predicted event can carry prediction_id (consumed by
    infra-api to create linked depot work orders)."""
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
    return {r.component: row[0] for r, row in zip(risks, rows)}


# --- fuel-monitoring: per-bus learned consumption from fuel.reading --------
# (BUSINESS_LOGIC_AUDIT §4). Pairs of consecutive readings with genuine
# distance and a genuine H2 drop yield kg/100km; refuel jumps and noise are
# rejected. fleet-api range math reads fleet.fuel_consumption.

MIN_PAIR_KM = 1.0     # ignore GPS/odo jitter
MAX_DROP_PCT = 30.0   # a larger "drop" is a refuel/sensor artifact


def consumption_kg_per_100km(prev_pct: float, pct: float, delta_km: float, capacity_kg: float) -> float | None:
    """kg/100km between two readings, or None when the pair is unusable."""
    if capacity_kg <= 0 or delta_km < MIN_PAIR_KM:
        return None
    drop = prev_pct - pct
    if drop <= 0 or drop > MAX_DROP_PCT:
        return None
    return capacity_kg * (drop / 100.0) / delta_km * 100.0


async def upsert_fuel_consumption(pool, bus_id: str, kg_per_100km: float, delta_km: float) -> None:
    await pool.execute(
        """
        INSERT INTO fleet.fuel_consumption (bus_id, kg_per_100km, sample_km, samples, updated_at)
        VALUES ($1::uuid, $2, $3, 1, now())
        ON CONFLICT (bus_id) DO UPDATE SET
            kg_per_100km = (fleet.fuel_consumption.kg_per_100km * fleet.fuel_consumption.samples
                            + EXCLUDED.kg_per_100km) / (fleet.fuel_consumption.samples + 1),
            sample_km = fleet.fuel_consumption.sample_km + EXCLUDED.sample_km,
            samples = fleet.fuel_consumption.samples + 1,
            updated_at = now()
        """,
        bus_id, kg_per_100km, delta_km,
    )


async def score_bus(pool, producer, bus_id: str, model) -> list[ComponentRisk]:
    features = await fetch_features(pool, bus_id, settings.feature_window_hours)
    if features is None:
        return []
    if getattr(model, "needs_sequence", False):
        seq = await fetch_sequence(pool, bus_id, settings.feature_window_hours)
        if seq is None:
            return []
        features["_sequence"] = seq
    risks = model.predict_all(features)
    prediction_ids = await persist_predictions(pool, bus_id, model.version, risks)
    for r in risks:
        if r.risk_score >= settings.high_risk_threshold:
            payload = build_envelope(
                {
                    "prediction_id": prediction_ids.get(r.component),
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
        settings.fuel_topic,
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
    fuel_state: dict[str, dict] = {}   # bus_id -> {"pct": float, "odo": float}
    capacity_cache: dict[str, float] = {}

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
                        data = env.get("data", {})
                        bus_id = data.get("bus_id")
                        if not bus_id:
                            continue
                        if rec.topic == settings.fuel_topic:
                            pct = data.get("h2_level_pct")
                            odo = data.get("odometer_km")
                            if pct is None or odo is None:
                                continue
                            prev = fuel_state.get(bus_id)
                            if prev is not None:
                                if bus_id not in capacity_cache:
                                    cap = await pool.fetchval(
                                        "SELECT COALESCE(h2_capacity_kg,0) FROM fleet.vehicles WHERE id = $1::uuid",
                                        bus_id)
                                    capacity_cache[bus_id] = float(cap or 0.0)
                                rate = consumption_kg_per_100km(
                                    prev["pct"], float(pct), float(odo) - prev["odo"], capacity_cache[bus_id])
                                if rate is not None:
                                    await upsert_fuel_consumption(
                                        pool, bus_id, rate, float(odo) - prev["odo"])
                            fuel_state[bus_id] = {"pct": float(pct), "odo": float(odo)}
                        else:
                            active_buses[bus_id] = now
                            # Derive the catalog fuel.reading stream (SPEC §3.3)
                            # from enriched telemetry and publish it; the fuel
                            # branch above consumes it into fleet.fuel_consumption.
                            if data.get("h2_level_pct") is not None and data.get("odometer_km") is not None:
                                fuel = build_envelope({
                                    "bus_id": bus_id,
                                    "ts": data.get("ts") or now.isoformat().replace("+00:00", "Z"),
                                    "h2_level_pct": data.get("h2_level_pct"),
                                    "odometer_km": data.get("odometer_km"),
                                }, topic=settings.fuel_topic)
                                await producer.send_and_wait(
                                    settings.fuel_topic, fuel, key=bus_id.encode())
                    except Exception:
                        log.warning("dropping malformed %s message", rec.topic)

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
