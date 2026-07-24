"""Tests for carbon-analytics period bounds, accounting math and event envelope.

Run: python -m pytest services/python/carbon-analytics/tests -q
"""

from __future__ import annotations

import asyncio
import datetime as dt
import json

import pytest

from app.config import settings
from app.core import PeriodResult, build_envelope, compute_period, period_bounds


class TestPeriodBounds:
    def test_regular_month(self):
        start, end = period_bounds("2025-01")
        assert start == dt.datetime(2025, 1, 1, tzinfo=dt.timezone.utc)
        assert end == dt.datetime(2025, 2, 1, tzinfo=dt.timezone.utc)

    def test_december_rolls_into_next_year(self):
        start, end = period_bounds("2025-12")
        assert start == dt.datetime(2025, 12, 1, tzinfo=dt.timezone.utc)
        assert end == dt.datetime(2026, 1, 1, tzinfo=dt.timezone.utc)

    def test_all_months_are_one_month_long(self):
        for month in range(1, 13):
            start, end = period_bounds(f"2025-{month:02d}")
            assert end > start
            assert (end - start).days in (28, 29, 30, 31)

    @pytest.mark.parametrize("bad", ["2025-1", "2025-13", "2025-00", "jan-2025", "202501", ""])
    def test_invalid_periods_rejected(self, bad):
        with pytest.raises(ValueError, match="YYYY-MM"):
            period_bounds(bad)


class TestBuildEnvelope:
    def test_envelope_shape(self):
        result = PeriodResult(
            period="2026-06",
            total_km=12_000.0,
            bus_count=50,
            kg_co2_avoided=14_400.0,
            credits=14.4,
            credit_id="11111111-1111-1111-1111-111111111111",
        )
        env = json.loads(build_envelope(result))
        assert env["type"] == settings.output_topic == "carbon.credit.issued"
        assert env["source"] == "carbon-analytics"
        assert env["id"] and env["time"]
        data = env["data"]
        assert data["period"] == "2026-06"
        assert data["kg_co2_avoided"] == 14_400.0
        assert data["credits"] == 14.4
        assert data["credit_id"] == result.credit_id


class _FakeConn:
    """Records the SQL executed inside the idempotent delete+insert transaction."""

    def __init__(self):
        self.statements: list[tuple[str, tuple]] = []

    def transaction(self):
        conn = self

        class _Tx:
            async def __aenter__(self):
                return conn

            async def __aexit__(self, *exc):
                return False

        return _Tx()

    async def execute(self, sql, *args):
        self.statements.append((sql, args))


class _FakeAcquire:
    def __init__(self, conn):
        self._conn = conn

    async def __aenter__(self):
        return self._conn

    async def __aexit__(self, *exc):
        return False


class _FakePool:
    def __init__(self, total_km: float, bus_count: int):
        self._row = {"total_km": total_km, "bus_count": bus_count}
        self.conn = _FakeConn()
        self.distance_args = None

    async def fetchrow(self, sql, *args):
        self.distance_args = args
        return self._row

    def acquire(self):
        return _FakeAcquire(self.conn)


class TestComputePeriod:
    def test_compute_logic_and_idempotent_write(self):
        # 10_000 fleet-km * 1.2 kg/km = 12_000 kg CO2 avoided = 12 credits.
        pool = _FakePool(total_km=10_000.0, bus_count=50)
        result = asyncio.run(compute_period(pool, "2026-06", publish=False))

        assert result.total_km == 10_000.0
        assert result.bus_count == 50
        assert result.kg_co2_avoided == pytest.approx(12_000.0)
        assert result.credits == pytest.approx(12.0)
        assert result.credit_id and not result.event_published

        # The distance query is scoped to the period window.
        start, end = period_bounds("2026-06")
        assert pool.distance_args == (start, end)

        # Idempotent write: delete-then-insert in ONE transaction.
        stmts = pool.conn.statements
        assert len(stmts) == 2
        assert "DELETE FROM citizen.carbon_credits WHERE period" in stmts[0][0]
        assert stmts[0][1] == ("2026-06",)
        assert "INSERT INTO citizen.carbon_credits" in stmts[1][0]
        assert stmts[1][1][1:] == ("2026-06", 12_000.0, 12.0)

    def test_zero_distance_period(self):
        pool = _FakePool(total_km=0.0, bus_count=0)
        result = asyncio.run(compute_period(pool, "2026-06", publish=False))
        assert result.kg_co2_avoided == 0.0
        assert result.credits == 0.0

    def test_invalid_period_raises_before_db(self):
        pool = _FakePool(total_km=1.0, bus_count=1)
        with pytest.raises(ValueError):
            asyncio.run(compute_period(pool, "not-a-period", publish=False))
        assert pool.conn.statements == []
