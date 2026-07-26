"""Wave-4 fuel-monitoring: per-bus learned H2 consumption (audit §4)."""

from __future__ import annotations

import pytest

from app.events import consumption_kg_per_100km


class TestConsumptionKgPer100km:
    def test_normal_drop(self):
        # 40kg tank, 10% drop over 50km -> 4kg/50km = 8 kg/100km
        rate = consumption_kg_per_100km(60.0, 50.0, 50.0, 40.0)
        assert rate == pytest.approx(8.0)

    def test_refuel_jump_rejected(self):
        # H2 rising = refuel, not consumption
        assert consumption_kg_per_100km(50.0, 80.0, 50.0, 40.0) is None

    def test_implausible_drop_rejected(self):
        # >30% between readings = sensor artifact
        assert consumption_kg_per_100km(90.0, 40.0, 50.0, 40.0) is None

    def test_too_short_segment_rejected(self):
        assert consumption_kg_per_100km(60.0, 55.0, 0.5, 40.0) is None

    def test_zero_capacity_rejected(self):
        assert consumption_kg_per_100km(60.0, 55.0, 50.0, 0.0) is None

    def test_boundary_drop_allowed(self):
        # exactly 30% drop is accepted
        rate = consumption_kg_per_100km(60.0, 30.0, 100.0, 40.0)
        assert rate == pytest.approx(12.0)
