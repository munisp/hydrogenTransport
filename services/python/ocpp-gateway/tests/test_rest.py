"""REST read API + healthz tests (JWT verification disabled via FastAPI
dependency override; a fail-closed 503 check without the override)."""

from __future__ import annotations

import asyncio

import httpx
import pytest

from app import main
from app.main import app
from conftest import FakePool, FakePublisher


@pytest.fixture(autouse=True)
def _clear_dependency_overrides():
    yield
    app.dependency_overrides.clear()


def _client(pool: FakePool) -> httpx.AsyncClient:
    app.state.pool = pool
    app.state.publisher = FakePublisher()
    app.state.active_charge_points = {"CP-LIVE-1": "2026-01-01T00:00:00Z"}
    return httpx.AsyncClient(transport=httpx.ASGITransport(app=app), base_url="http://test")


async def _override_auth():
    return {"sub": "test-operator", "realm_access": {"roles": ["operator"]}}


def run(coro):
    return asyncio.run(coro)


def _seed(pool: FakePool):
    pool._upsert_cp("CP-A", "VendorA", "M1")
    pool._upsert_cp("CP-B", "VendorB", "M2")


class TestChargePoints:
    def test_list_charge_points(self):
        pool = FakePool()
        _seed(pool)
        app.dependency_overrides[main.require_auth] = _override_auth

        async def go():
            async with _client(pool) as client:
                return await client.get("/v1/ocpp/charge-points")

        resp = run(go())
        assert resp.status_code == 200
        body = resp.json()
        assert body["count"] == 2
        ids = {cp["ocpp_id"] for cp in body["charge_points"]}
        assert ids == {"CP-A", "CP-B"}

    def test_get_charge_point(self):
        pool = FakePool()
        _seed(pool)
        app.dependency_overrides[main.require_auth] = _override_auth

        async def go():
            async with _client(pool) as client:
                return await client.get("/v1/ocpp/charge-points/CP-A")

        resp = run(go())
        assert resp.status_code == 200
        assert resp.json()["vendor"] == "VendorA"

    def test_get_charge_point_404(self):
        pool = FakePool()
        app.dependency_overrides[main.require_auth] = _override_auth

        async def go():
            async with _client(pool) as client:
                return await client.get("/v1/ocpp/charge-points/NOPE")

        assert run(go()).status_code == 404

    def test_requires_auth_fail_closed_without_keycloak(self):
        # No override and KEYCLOAK_ISSUER unset in the test env -> 503
        # (fail-closed, per the shared h2fleet_auth contract).
        pool = FakePool()

        async def go():
            async with _client(pool) as client:
                return await client.get("/v1/ocpp/charge-points")

        assert run(go()).status_code == 503


class TestSessions:
    def _pool_with_sessions(self):
        pool = FakePool()
        pool._upsert_cp("CP-A", "VendorA", "M1")
        cp = pool.charge_points["CP-A"]
        sid1 = "s-1"
        pool.sessions[sid1] = {
            "id": sid1, "charge_point_id": cp["id"], "bus_id": None,
            "connector_id": 1, "id_tag": "T1", "meter_start": 0.0,
            "meter_stop": None, "kwh": None, "status": "active",
        }
        sid2 = "s-2"
        pool.sessions[sid2] = {
            "id": sid2, "charge_point_id": cp["id"], "bus_id": None,
            "connector_id": 1, "id_tag": "T2", "meter_start": 10.0,
            "meter_stop": 20.0, "kwh": 10.0, "status": "completed",
        }
        return pool

    def test_list_sessions_all_and_status_filter(self):
        pool = self._pool_with_sessions()
        app.dependency_overrides[main.require_auth] = _override_auth

        async def go():
            async with _client(pool) as client:
                all_r = await client.get("/v1/ocpp/sessions")
                active_r = await client.get("/v1/ocpp/sessions", params={"status": "active"})
                return all_r, active_r

        all_r, active_r = run(go())
        assert all_r.json()["count"] == 2
        body = active_r.json()
        assert body["count"] == 1
        assert body["sessions"][0]["id"] == "s-1"

    def test_list_sessions_station_filter(self):
        pool = self._pool_with_sessions()
        app.dependency_overrides[main.require_auth] = _override_auth
        station_id = pool.station_id

        async def go():
            async with _client(pool) as client:
                hit = await client.get("/v1/ocpp/sessions", params={"station_id": station_id})
                miss = await client.get(
                    "/v1/ocpp/sessions",
                    params={"station_id": "00000000-0000-0000-0000-000000000000"},
                )
                return hit, miss

        hit, miss = run(go())
        assert hit.json()["count"] == 2
        assert miss.json()["count"] == 0


class TestHealthz:
    def test_healthz_ok_reports_db_and_ws(self):
        pool = FakePool()

        async def go():
            async with _client(pool) as client:
                return await client.get("/healthz")

        resp = run(go())
        body = resp.json()
        assert body["status"] == "ok"
        assert body["service"] == "ocpp-gateway"
        assert body["db"] is True
        assert body["websocket"]["path"] == "/ocpp/{charge_point_id}"
        assert body["websocket"]["active_charge_points"] == 1

    def test_healthz_degraded_when_db_down(self):
        pool = FakePool(fail=True)

        async def go():
            async with _client(pool) as client:
                return await client.get("/healthz")

        body = run(go()).json()
        assert body["status"] == "degraded"
        assert body["db"] is False
