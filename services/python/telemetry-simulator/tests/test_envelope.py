"""Validate the simulator's telemetry.raw envelope against the canonical
JSON schema (packages/events/schemas/telemetry.raw.json, SPEC §3.3) and pin
the state model's plausibility invariants.

Run: python -m pytest services/python/telemetry-simulator/tests -q
"""

from __future__ import annotations

import json
import random
import uuid
from pathlib import Path

import pytest
from jsonschema import Draft202012Validator, FormatChecker

from app.simulator import envelope
from app.state import BusState

SCHEMA_PATH = (
    Path(__file__).resolve().parents[4] / "packages" / "events" / "schemas" / "telemetry.raw.json"
)
SCHEMA = json.loads(SCHEMA_PATH.read_text())
VALIDATOR = Draft202012Validator(SCHEMA, format_checker=FormatChecker())


def make_bus(status: str = "active") -> BusState:
    return BusState(
        bus_id=str(uuid.uuid4()),
        fleet_no="H2-001",
        status=status,
        lat=52.52,
        lon=13.405,
        route_id="R1",
        depot_id="depot-central",
    )


def test_schema_file_is_the_canonical_raw_schema():
    assert SCHEMA["$id"].endswith("/telemetry.raw.json")
    assert SCHEMA["properties"]["type"]["const"] == "telemetry.raw"


def test_envelope_conforms_to_schema():
    env = envelope(make_bus())
    errors = list(VALIDATOR.iter_errors(env))
    assert errors == [], f"envelope violates telemetry.raw schema: {errors}"


def test_envelope_conforms_after_simulation_steps():
    # Drive the state model hard and validate every emitted envelope: values
    # must stay inside the schema bounds (0-100%, lat/lon ranges, ...).
    random.seed(42)
    bus = make_bus()
    for _ in range(200):
        bus.step(dt_seconds=1.0, drain_pct_per_km=0.5, refuel_threshold=15.0)
        env = envelope(bus)
        errors = list(VALIDATOR.iter_errors(env))
        assert errors == [], f"post-step envelope violates schema: {errors}"


def test_envelope_identity_fields():
    bus = make_bus()
    env = envelope(bus)
    assert env["type"] == "telemetry.raw"
    assert env["source"]
    uuid.UUID(env["id"])  # parses as uuid
    assert env["data"]["bus_id"] == bus.bus_id
    uuid.UUID(env["data"]["bus_id"])
    # Fleet context rides along under additionalProperties: true.
    assert env["data"]["fleet_no"] == "H2-001"
    assert env["data"]["route_id"] == "R1"


def test_envelope_bus_id_must_be_uuid_for_schema():
    bus = make_bus()
    bus.bus_id = "not-a-uuid"
    errors = list(VALIDATOR.iter_errors(envelope(bus)))
    assert errors, "non-uuid bus_id must fail schema validation"


@pytest.mark.parametrize("status,expect_motion", [("active", True), ("depot", False), ("maintenance", False)])
def test_state_model_speed_invariants(status, expect_motion):
    random.seed(7)
    bus = make_bus(status=status)
    speeds = []
    for _ in range(50):
        bus.step(dt_seconds=1.0, drain_pct_per_km=0.5, refuel_threshold=15.0)
        speeds.append(bus.speed_kph)
    assert all(0.0 <= s <= 50.0 for s in speeds), "speed clamp 0-50 kph violated"
    if expect_motion:
        assert max(speeds) > 0.0, "active bus should move"
    else:
        assert all(s == 0.0 for s in speeds), f"{status} bus must stay parked"
    assert 0.0 <= bus.h2_level_pct <= 100.0
    assert 0.0 <= bus.battery_soc_pct <= 100.0
