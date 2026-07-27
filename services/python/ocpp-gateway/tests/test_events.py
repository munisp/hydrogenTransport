"""Event envelope + status-mapping unit tests (packages/events conventions)."""

from __future__ import annotations

import json

import pytest

from app.csms import to_station_status
from app.events import build_envelope, build_status_changed


class TestEnvelope:
    def test_cloudevents_shape(self):
        env = json.loads(build_envelope("station.status.changed", {"k": 1}))
        assert set(env) == {"id", "type", "source", "time", "data"}
        assert env["type"] == "station.status.changed"
        assert env["source"] == "ocpp-gateway"
        assert env["data"] == {"k": 1}
        assert env["time"].endswith("Z")

    def test_status_changed_required_and_additive_fields(self):
        data = build_status_changed(
            station_id="st-1",
            status="online",
            charge_point_id="cp-1",
            ocpp_id="CP-1",
            ocpp_status="Charging",
            available_kwh=120.0,
        )
        # schema-required fields
        assert data["station_id"] == "st-1"
        assert data["status"] == "online"
        assert data["available_kg"] == 0.0
        # wave-5 additive contract fields
        assert data["station_type"] == "ev_charger"
        assert data["available_kwh"] == 120.0
        # traceability extras
        assert data["charge_point_id"] == "cp-1"
        assert data["ocpp_id"] == "CP-1"
        assert data["ocpp_status"] == "Charging"

    def test_available_kwh_omitted_when_unknown(self):
        data = build_status_changed(
            station_id="st-1",
            status="offline",
            charge_point_id="cp-1",
            ocpp_id="CP-1",
            ocpp_status="Unavailable",
        )
        assert "available_kwh" not in data


class TestStationStatusMapping:
    @pytest.mark.parametrize(
        "ocpp_status,expected",
        [
            ("Available", "online"),
            ("Preparing", "online"),
            ("Charging", "online"),
            ("SuspendedEV", "online"),
            ("SuspendedEVSE", "online"),
            ("Finishing", "online"),
            ("Reserved", "online"),
            ("Unavailable", "offline"),
            ("Faulted", "degraded"),
            ("SomethingNew", "online"),
        ],
    )
    def test_mapping(self, ocpp_status, expected):
        assert to_station_status(ocpp_status) == expected
