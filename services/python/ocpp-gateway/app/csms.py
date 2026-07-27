"""OCPP 1.6J central-system (CSMS) message handlers — core profile.

One ``ChargePointHandler`` instance exists per connected charge point
(WebSocket /ocpp/{charge_point_id}). Supported charge-point-initiated
messages: BootNotification, Heartbeat, StatusNotification, Authorize,
StartTransaction, MeterValues, StopTransaction. Persistence goes to
infra.charge_points / infra.charging_sessions (wave-5 schema contract) and
status changes are published to Kafka as station.status.changed envelopes.

Transaction-id mapping: OCPP transaction ids are scoped to a charge point
and only need to be unique per connection, so they come from a per-instance
counter; the mapping {transaction_id: infra.charging_sessions.id} lives on
the instance and is lost on disconnect (documented v1 limitation — a
reconnected charger must start a new transaction).
"""

from __future__ import annotations

import datetime as dt
import itertools
import logging

from ocpp.routing import on
from ocpp.v16 import ChargePoint as _BaseChargePoint
from ocpp.v16 import call_result
from ocpp.v16.enums import Action, AuthorizationStatus, RegistrationStatus

from . import db
from .events import build_status_changed

log = logging.getLogger("ocpp-gateway.csms")

# OCPP 1.6 ChargePointStatus -> station-domain status enum used by
# station.status.changed (online|offline|maintenance|degraded).
STATION_STATUS_MAP = {
    "Available": "online",
    "Preparing": "online",
    "Charging": "online",
    "SuspendedEV": "online",
    "SuspendedEVSE": "online",
    "Finishing": "online",
    "Reserved": "online",
    "Unavailable": "offline",
    "Faulted": "degraded",
}


def to_station_status(ocpp_status: str) -> str:
    """Map a raw OCPP status onto the station.status.changed status enum."""
    return STATION_STATUS_MAP.get(ocpp_status, "online")


def _utcnow_iso() -> str:
    return dt.datetime.now(dt.timezone.utc).isoformat().replace("+00:00", "Z")


def _energy_register_kwh_factor(sample: dict, default_factor: float) -> float:
    """Unit-aware factor for one sampledValue (Energy.Active.Import.Register
    defaults to Wh; an explicit unit='kWh' is honoured)."""
    unit = str(sample.get("unit") or "").lower()
    if unit == "kwh":
        return 1.0
    return default_factor


class ChargePointHandler(_BaseChargePoint):
    """OCPP 1.6J charge-point connection handler (core profile)."""

    def __init__(self, ocpp_id: str, connection, *, pool, publisher, settings) -> None:
        super().__init__(ocpp_id, connection)
        self.ocpp_id = ocpp_id
        self.pool = pool
        self.publisher = publisher
        self.settings = settings
        self._tx_ids = itertools.count(1)
        # OCPP transaction_id -> infra.charging_sessions.id (per connection).
        self._transactions: dict[int, str] = {}
        self._pk: str | None = None

    # ------------------------------------------------------------- helpers
    async def _pk_or_fetch(self) -> str | None:
        """infra.charge_points.id for this connection (cached after boot)."""
        if self._pk is None:
            self._pk = await db.get_charge_point_pk(self.pool, self.ocpp_id)
        return self._pk

    async def _authorize(self, id_tag: str) -> tuple[AuthorizationStatus, str | None]:
        """Authorize policy: known bus id_tags are always accepted; otherwise
        the OCPP_OPEN_ID_TAGS whitelist applies ('*' = open charging, dev).

        Returns (status, bus_id|None)."""
        bus_id = await db.find_bus_id(self.pool, id_tag)
        if bus_id is not None:
            return AuthorizationStatus.accepted, bus_id
        if self.settings.open_charging:
            log.warning(
                "OPEN CHARGING active (OCPP_OPEN_ID_TAGS='*'): accepting unknown "
                "id_tag %r on %s — set a whitelist in production",
                id_tag,
                self.ocpp_id,
            )
            return AuthorizationStatus.accepted, None
        if id_tag in self.settings.open_id_tags:
            return AuthorizationStatus.accepted, None
        log.info("id_tag %r rejected on %s (not a known bus, not whitelisted)", id_tag, self.ocpp_id)
        return AuthorizationStatus.invalid, None

    # ------------------------------------------------------------- handlers
    @on(Action.BootNotification)
    async def on_boot_notification(
        self, charge_point_vendor: str, charge_point_model: str, **kwargs
    ):
        row = await db.upsert_charge_point(
            self.pool, self.ocpp_id, charge_point_vendor, charge_point_model
        )
        if row:
            self._pk = row["id"]
        log.info(
            "boot accepted: ocpp_id=%s vendor=%s model=%s",
            self.ocpp_id, charge_point_vendor, charge_point_model,
        )
        return call_result.BootNotificationPayload(
            current_time=_utcnow_iso(),
            interval=self.settings.ocpp_boot_interval,
            status=RegistrationStatus.accepted,
        )

    @on(Action.Heartbeat)
    async def on_heartbeat(self, **kwargs):
        await db.touch_heartbeat(self.pool, self.ocpp_id)
        return call_result.HeartbeatPayload(current_time=_utcnow_iso())

    @on(Action.StatusNotification)
    async def on_status_notification(
        self, connector_id: int, error_code: str, status: str, **kwargs
    ):
        row = await db.set_status(self.pool, self.ocpp_id, status)
        log.info("status: ocpp_id=%s connector=%s status=%s", self.ocpp_id, connector_id, status)
        if row and row.get("station_id") and self.publisher is not None:
            data = build_status_changed(
                station_id=row["station_id"],
                status=to_station_status(status),
                charge_point_id=row["id"],
                ocpp_id=self.ocpp_id,
                ocpp_status=status,
                available_kwh=row.get("available_kwh"),
            )
            await self.publisher.publish(data, key=row["station_id"])
        elif not (row and row.get("station_id")):
            log.info(
                "status event skipped for %s: charge point not linked to a station",
                self.ocpp_id,
            )
        return call_result.StatusNotificationPayload()

    @on(Action.Authorize)
    async def on_authorize(self, id_tag: str, **kwargs):
        status, _bus_id = await self._authorize(id_tag)
        return call_result.AuthorizePayload(id_tag_info={"status": status})

    @on(Action.StartTransaction)
    async def on_start_transaction(
        self, connector_id: int, id_tag: str, meter_start: float, timestamp: str, **kwargs
    ):
        auth, bus_id = await self._authorize(id_tag)
        if auth != AuthorizationStatus.accepted:
            return call_result.StartTransactionPayload(
                transaction_id=0, id_tag_info={"status": auth}
            )
        pk = await self._pk_or_fetch()
        if pk is None:
            log.warning("StartTransaction before BootNotification on %s — rejected", self.ocpp_id)
            return call_result.StartTransactionPayload(
                transaction_id=0,
                id_tag_info={"status": AuthorizationStatus.invalid},
            )
        session_id = await db.start_session(
            self.pool, pk, connector_id, id_tag, bus_id, float(meter_start)
        )
        tx_id = next(self._tx_ids)
        self._transactions[tx_id] = session_id
        log.info(
            "transaction started: ocpp_id=%s tx=%s session=%s bus=%s",
            self.ocpp_id, tx_id, session_id, bus_id,
        )
        return call_result.StartTransactionPayload(
            transaction_id=tx_id, id_tag_info={"status": auth}
        )

    @on(Action.MeterValues)
    async def on_meter_values(
        self, connector_id: int, meter_value: list, transaction_id: int | None = None, **kwargs
    ):
        session_id = self._transactions.get(transaction_id) if transaction_id is not None else None
        if session_id is not None:
            sample = _energy_register_sample(meter_value)
            if sample is not None:
                try:
                    register = float(sample["value"])
                except (TypeError, ValueError):
                    register = None
                if register is not None:
                    factor = _energy_register_kwh_factor(sample, self.settings.kwh_factor)
                    await db.update_session_kwh(self.pool, session_id, register, factor)
        return call_result.MeterValuesPayload()

    @on(Action.StopTransaction)
    async def on_stop_transaction(
        self, meter_stop: float, timestamp: str, transaction_id: int, **kwargs
    ):
        session_id = self._transactions.pop(transaction_id, None)
        if session_id is None:
            log.warning(
                "StopTransaction for unknown tx=%s on %s (connection restart?) — acked",
                transaction_id, self.ocpp_id,
            )
            return call_result.StopTransactionPayload(
                id_tag_info={"status": AuthorizationStatus.accepted}
            )
        row = await db.stop_session(
            self.pool, session_id, float(meter_stop), self.settings.kwh_factor
        )
        log.info(
            "transaction stopped: ocpp_id=%s tx=%s session=%s kwh=%s",
            self.ocpp_id, transaction_id, session_id, row.get("kwh") if row else None,
        )
        return call_result.StopTransactionPayload(
            id_tag_info={"status": AuthorizationStatus.accepted}
        )


def _energy_register_sample(meter_value: list) -> dict | None:
    """First Energy.Active.Import.Register sample in a MeterValues meterValue
    list (the default measurand when omitted, per OCPP 1.6)."""
    for entry in meter_value or []:
        for sample in entry.get("sampled_value") or entry.get("sampledValue") or []:
            measurand = sample.get("measurand")
            if measurand in (None, "Energy.Active.Import.Register"):
                return sample
    return None
