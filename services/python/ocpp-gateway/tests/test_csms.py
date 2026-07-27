"""End-to-end CSMS tests: a mock OCPP 1.6J charge point (the real `ocpp`
library client) talks to the real ChargePointHandler over real websockets;
Postgres and Kafka are faked in-memory.

Run: python -m pytest services/python/ocpp-gateway/tests -q
"""

from __future__ import annotations

import asyncio

import websockets
from ocpp.v16 import ChargePoint as _ClientBase
from ocpp.v16 import call

from app.config import Settings
from app.csms import ChargePointHandler
from conftest import FakePool, FakePublisher

CP_ID = "CP-TEST-1"
TS = "2026-01-01T00:00:00Z"


class MockChargePoint(_ClientBase):
    """Charge-point side: no CS->CP messages to handle in these tests."""


async def _serve(pool, publisher, settings, scenario):
    async def handler(ws):
        cp = ChargePointHandler(CP_ID, ws, pool=pool, publisher=publisher, settings=settings)
        try:
            await cp.start()
        except websockets.exceptions.ConnectionClosed:
            pass

    async with websockets.serve(handler, "127.0.0.1", 0, subprotocols=["ocpp1.6"]) as server:
        port = server.sockets[0].getsockname()[1]
        async with websockets.connect(
            f"ws://127.0.0.1:{port}/ocpp/{CP_ID}", subprotocols=["ocpp1.6"]
        ) as ws:
            client = MockChargePoint(CP_ID, ws)
            task = asyncio.create_task(client.start())
            try:
                await scenario(client)
            finally:
                task.cancel()


def run(scenario, *, pool=None, publisher=None, settings=None):
    pool = pool or FakePool()
    publisher = publisher or FakePublisher()
    settings = settings or Settings()
    asyncio.run(_serve(pool, publisher, settings, scenario))
    return pool, publisher


async def _boot(client, vendor="VendorX", model="Model Y"):
    return await client.call(
        call.BootNotificationPayload(
            charge_point_vendor=vendor, charge_point_model=model, firmware_version="1.4.2"
        )
    )


# --------------------------------------------------------------------- boot
def test_boot_accepted_and_row_upserted():
    async def scenario(client):
        resp = await _boot(client)
        assert resp.status == "Accepted"
        assert resp.interval == 300
        assert resp.current_time

    pool, _ = run(scenario)
    cp = pool.charge_points[CP_ID]
    assert cp["vendor"] == "VendorX"
    assert cp["model"] == "Model Y"
    assert cp["status"] == "Available"
    assert cp["last_heartbeat"] == "now"


def test_reboot_updates_existing_row_in_place():
    async def scenario(client):
        await _boot(client)
        resp = await _boot(client, vendor="VendorX", model="Model Z")
        assert resp.status == "Accepted"

    pool, _ = run(scenario)
    assert len(pool.charge_points) == 1
    assert pool.charge_points[CP_ID]["model"] == "Model Z"
    assert pool.charge_points[CP_ID]["status"] == "Available"


def test_boot_interval_is_configurable():
    async def scenario(client):
        resp = await _boot(client)
        assert resp.interval == 60

    run(scenario, settings=Settings(ocpp_boot_interval=60))


# ---------------------------------------------------------------- heartbeat
def test_heartbeat_updates_last_heartbeat():
    async def scenario(client):
        resp = await client.call(call.HeartbeatPayload())
        assert resp.current_time

    pool, _ = run(scenario)
    assert pool.heartbeats == 1


# ------------------------------------------------------------------- status
def test_status_notification_updates_row_and_publishes_event():
    async def scenario(client):
        await _boot(client)
        resp = await client.call(
            call.StatusNotificationPayload(connector_id=1, error_code="NoError", status="Charging")
        )
        assert resp is not None

    pool, publisher = run(scenario)
    assert pool.charge_points[CP_ID]["status"] == "Charging"
    assert len(publisher.published) == 1
    data, key = publisher.published[0]
    cp = pool.charge_points[CP_ID]
    assert key == cp["station_id"]
    assert data["station_id"] == cp["station_id"]
    assert data["status"] == "online"          # station-domain mapping
    assert data["ocpp_status"] == "Charging"   # raw OCPP status (additive)
    assert data["station_type"] == "ev_charger"
    assert data["ocpp_id"] == CP_ID


def test_status_notification_station_status_mapping():
    async def scenario(client):
        await _boot(client)
        for status in ("Unavailable", "Faulted"):
            await client.call(
                call.StatusNotificationPayload(connector_id=1, error_code="NoError", status=status)
            )

    _, publisher = run(scenario)
    assert [d["status"] for d, _ in publisher.published] == ["offline", "degraded"]


def test_status_notification_without_station_link_skips_event():
    pool = FakePool(station_id=None)
    pool.station_id = None

    async def scenario(client):
        await _boot(client)
        await client.call(
            call.StatusNotificationPayload(connector_id=1, error_code="NoError", status="Available")
        )

    pool, publisher = run(scenario, pool=pool)
    assert pool.charge_points[CP_ID]["status"] == "Available"
    assert publisher.published == []


def test_available_kwh_included_when_known():
    async def scenario(client):
        await _boot(client)
        await client.call(
            call.StatusNotificationPayload(connector_id=1, error_code="NoError", status="Available")
        )

    _, publisher = run(scenario, pool=FakePool(available_kwh=180.5))
    assert publisher.published[0][0]["available_kwh"] == 180.5


# ---------------------------------------------------------------- authorize
def test_authorize_open_charging_accepts_unknown_tag():
    async def scenario(client):
        resp = await client.call(call.AuthorizePayload(id_tag="RANDOM-TAG"))
        assert resp.id_tag_info["status"] == "Accepted"

    run(scenario, settings=Settings(ocpp_open_id_tags="*"))


def test_authorize_closed_known_bus_accepted():
    pool = FakePool(known_buses={"BUS-42": "11111111-1111-1111-1111-111111111111"})

    async def scenario(client):
        resp = await client.call(call.AuthorizePayload(id_tag="BUS-42"))
        assert resp.id_tag_info["status"] == "Accepted"

    run(scenario, pool=pool, settings=Settings(ocpp_open_id_tags="DEPOT-CARD-1"))


def test_authorize_closed_whitelisted_tag_accepted_unknown_rejected():
    async def scenario(client):
        ok = await client.call(call.AuthorizePayload(id_tag="DEPOT-CARD-1"))
        assert ok.id_tag_info["status"] == "Accepted"
        bad = await client.call(call.AuthorizePayload(id_tag="STRANGER"))
        assert bad.id_tag_info["status"] == "Invalid"

    run(scenario, settings=Settings(ocpp_open_id_tags="DEPOT-CARD-1,DEPOT-CARD-2"))


# ------------------------------------------------------------- transactions
async def _start(client, id_tag="BUS-42", meter_start=10_000):
    return await client.call(
        call.StartTransactionPayload(
            connector_id=1, id_tag=id_tag, meter_start=meter_start, timestamp=TS
        )
    )


def test_transaction_lifecycle_kwh_math():
    """start (10_000 Wh) -> meter values (12_500 Wh) -> stop (15_000 Wh):
    running kwh 2.5, final kwh 5.0 (default OCPP_METER_UNIT=wh)."""
    pool = FakePool(known_buses={"BUS-42": "bus-uuid-42"})

    async def scenario(client):
        await _boot(client)
        started = await _start(client)
        assert started.transaction_id == 1
        assert started.id_tag_info["status"] == "Accepted"

        mv = await client.call(
            call.MeterValuesPayload(
                connector_id=1,
                transaction_id=started.transaction_id,
                meter_value=[{"timestamp": TS, "sampledValue": [{"value": "12500"}]}],
            )
        )
        assert mv is not None

        stopped = await client.call(
            call.StopTransactionPayload(
                transaction_id=started.transaction_id, meter_stop=15_000, timestamp=TS
            )
        )
        assert stopped.id_tag_info["status"] == "Accepted"

    pool, _ = run(scenario, pool=pool)
    assert len(pool.sessions) == 1
    session = next(iter(pool.sessions.values()))
    assert session["status"] == "completed"
    assert session["bus_id"] == "bus-uuid-42"
    assert session["meter_start"] == 10_000
    assert session["meter_stop"] == 15_000
    assert session["kwh"] == 5.0


def test_meter_values_update_running_kwh_before_stop():
    pool = FakePool()

    async def scenario(client):
        await _boot(client)
        started = await _start(client, id_tag="TAG", meter_start=0)
        await client.call(
            call.MeterValuesPayload(
                connector_id=1,
                transaction_id=started.transaction_id,
                meter_value=[{"timestamp": TS, "sampledValue": [{"value": "12500"}]}],
            )
        )

    pool, _ = run(scenario, pool=pool)
    session = next(iter(pool.sessions.values()))
    assert session["kwh"] == 12.5
    assert session["status"] == "active"


def test_meter_values_explicit_kwh_unit_honored():
    pool = FakePool()

    async def scenario(client):
        await _boot(client)
        started = await _start(client, id_tag="TAG", meter_start=10)
        await client.call(
            call.MeterValuesPayload(
                connector_id=1,
                transaction_id=started.transaction_id,
                meter_value=[{
                    "timestamp": TS,
                    "sampledValue": [{
                        "value": "12.5",
                        "measurand": "Energy.Active.Import.Register",
                        "unit": "kWh",
                    }],
                }],
            )
        )

    pool, _ = run(scenario, pool=pool)
    assert next(iter(pool.sessions.values()))["kwh"] == 2.5


def test_start_transaction_rejected_for_unauthorized_tag():
    async def scenario(client):
        await _boot(client)
        resp = await _start(client, id_tag="STRANGER")
        assert resp.transaction_id == 0
        assert resp.id_tag_info["status"] == "Invalid"

    pool, _ = run(scenario, settings=Settings(ocpp_open_id_tags="DEPOT-CARD-1"))
    assert pool.sessions == {}


def test_stop_transaction_unknown_id_acked_without_crash():
    async def scenario(client):
        await _boot(client)
        resp = await client.call(
            call.StopTransactionPayload(transaction_id=999, meter_stop=42, timestamp=TS)
        )
        assert resp.id_tag_info["status"] == "Accepted"

    pool, _ = run(scenario)
    assert pool.sessions == {}
