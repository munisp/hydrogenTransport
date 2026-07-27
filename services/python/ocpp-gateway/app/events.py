"""Kafka event publishing — platform envelope conventions (SPEC.md §3.3,
packages/events README): every message is a CloudEvents-ish JSON object

    { "id": "<uuid>", "type": "<topic>", "source": "<service>",
      "time": "<rfc3339>", "data": { ... } }

with ``type`` equal to the topic name and the message key set to the entity
id. Mirrors the build_envelope/AIOKafkaProducer pattern used by
carbon-analytics (services/python/carbon-analytics/app/core.py), wrapped as a
long-lived lazily-connected producer for the WebSocket hot path.

station.status.changed (packages/events/schemas/station.status.changed.json):
data requires station_id (uuid), status (online|offline|maintenance|degraded),
available_kg. The wave-5 contract extends it ADDITIVELY with station_type and
available_kwh; we also add ocpp_id / charge_point_id / ocpp_status for
traceability (data.additionalProperties=true).
"""

from __future__ import annotations

import datetime as dt
import json
import logging
import uuid

log = logging.getLogger("ocpp-gateway.events")

SERVICE_NAME = "ocpp-gateway"


def _utcnow_iso() -> str:
    return dt.datetime.now(dt.timezone.utc).isoformat().replace("+00:00", "Z")


def build_envelope(topic: str, data: dict) -> bytes:
    """CloudEvents-ish envelope bytes (same shape as carbon-analytics)."""
    return json.dumps(
        {
            "id": str(uuid.uuid4()),
            "type": topic,
            "source": SERVICE_NAME,
            "time": _utcnow_iso(),
            "data": data,
        }
    ).encode()


def build_status_changed(
    *,
    station_id: str,
    status: str,
    charge_point_id: str,
    ocpp_id: str,
    ocpp_status: str,
    available_kwh: float | None = None,
) -> dict:
    """station.status.changed data payload for an EV charge point.

    ``status`` is the station-domain status (online|offline|degraded);
    ``available_kg`` is required by the current schema and is 0 for an EV
    charger (no hydrogen inventory). Additive wave-5 contract fields:
    station_type='ev_charger' and available_kwh when known.
    """
    data: dict = {
        "station_id": station_id,
        "status": status,
        "available_kg": 0.0,
        "station_type": "ev_charger",
        "charge_point_id": charge_point_id,
        "ocpp_id": ocpp_id,
        "ocpp_status": ocpp_status,
    }
    if available_kwh is not None:
        data["available_kwh"] = available_kwh
    return data


class EventPublisher:
    """Long-lived aiokafka producer with lazy connect + graceful degrade.

    The OCPP write path (DB) must survive a Kafka outage, so publish failures
    are logged and swallowed; /healthz reports producer connectivity.
    """

    def __init__(self, brokers: str, topic: str) -> None:
        self._brokers = brokers
        self.topic = topic
        self._producer = None
        self.connected = False

    async def _ensure(self) -> None:
        if self._producer is not None:
            return
        from aiokafka import AIOKafkaProducer  # deferred: import cost

        producer = AIOKafkaProducer(bootstrap_servers=self._brokers, acks="all")
        await producer.start()
        self._producer = producer
        self.connected = True
        log.info("kafka producer connected (brokers=%s topic=%s)", self._brokers, self.topic)

    async def publish(self, data: dict, key: str) -> bool:
        """Publish one envelope; returns False (and logs) on failure."""
        try:
            await self._ensure()
            await self._producer.send_and_wait(
                self.topic, build_envelope(self.topic, data), key=key.encode()
            )
            return True
        except Exception as exc:  # Kafka outage must not break the CSMS path
            self.connected = False
            log.error("kafka publish to %s failed (dropped event): %s", self.topic, exc)
            if self._producer is not None:
                try:
                    await self._producer.stop()
                except Exception:
                    pass
                self._producer = None
            return False

    async def aclose(self) -> None:
        if self._producer is not None:
            try:
                await self._producer.stop()
            finally:
                self._producer = None
                self.connected = False
