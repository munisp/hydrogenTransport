"""Tests for route-stop loading: fleet.route_stops (migration 0005) with the
deterministic per-date generator as fallback (BUSINESS_LOGIC_AUDIT §5).

Run: python -m pytest services/python/route-optimizer/tests -q
"""

from __future__ import annotations

import asyncio
import datetime as dt

import asyncpg

from app.data import generate_stops, load_stops


class _FakePool:
    def __init__(self, rows=None, exc=None):
        self._rows = rows or []
        self._exc = exc
        self.queries: list[str] = []

    async def fetch(self, sql, *args):
        self.queries.append(sql)
        if self._exc:
            raise self._exc
        return self._rows


class TestLoadStops:
    def test_uses_route_stops_table_when_populated(self):
        rows = [
            {"stop_id": "S001", "name": "Central Station", "lat": 52.52, "lon": 13.405},
            {"stop_id": "S002", "name": "Museum Island", "lat": 52.5169, "lon": 13.401},
        ]
        stops = asyncio.run(load_stops(_FakePool(rows=rows), dt.date(2026, 7, 24)))
        assert [s.stop_id for s in stops] == ["S001", "S002"]
        assert stops[0].name == "Central Station"
        assert stops[0].lat == 52.52 and stops[0].lon == 13.405

    def test_falls_back_to_generator_when_table_missing(self):
        pool = _FakePool(exc=asyncpg.exceptions.UndefinedTableError("relation fleet.route_stops does not exist"))
        stops = asyncio.run(load_stops(pool, dt.date(2026, 7, 24)))
        assert [s.stop_id for s in stops] == [s.stop_id for s in generate_stops(dt.date(2026, 7, 24))]

    def test_falls_back_to_generator_when_network_empty(self):
        stops = asyncio.run(load_stops(_FakePool(rows=[]), dt.date(2026, 7, 24)))
        assert len(stops) == len(generate_stops(dt.date(2026, 7, 24)))

    def test_generator_is_deterministic_per_date(self):
        a = generate_stops(dt.date(2026, 7, 24))
        b = generate_stops(dt.date(2026, 7, 24))
        assert [s.model_dump() for s in a] == [s.model_dump() for s in b]
