"""Inference endpoint tests via FastAPI TestClient against the REAL trained
artifacts shipped under ml-platform/artifacts (JWT dependency overridden;
auth failure semantics tested separately)."""

from __future__ import annotations

import os
from datetime import datetime, timedelta, timezone

import numpy as np
import pytest
from fastapi.testclient import TestClient

ARTIFACTS = os.environ.get("MODEL_ARTIFACTS_DIR", "")

pytestmark = pytest.mark.skipif(
    not os.path.isdir(os.path.join(ARTIFACTS, "maintenance_lstm")),
    reason="trained artifacts not present")


@pytest.fixture(scope="module")
def client():
    from app.main import app, jwt_verifier

    app.dependency_overrides[jwt_verifier.require_auth] = lambda: {"sub": "test"}
    with TestClient(app) as c:
        yield c
    app.dependency_overrides.clear()


def _maintenance_window(n=48):
    rng = np.random.default_rng(1)
    rows = np.column_stack([
        rng.uniform(30, 90, n),    # h2_level_pct
        rng.uniform(20, 90, n),    # fuel_cell_kw
        rng.uniform(30, 95, n),    # battery_soc_pct
        rng.uniform(0, 60, n),     # speed_kph
        rng.uniform(-5, 30, n),    # ambient_temp_c
    ])
    return rows.round(3).tolist()


def test_healthz(client):
    r = client.get("/healthz")
    assert r.status_code == 200
    body = r.json()
    assert body["status"] == "ok"
    assert all(body["models_loaded"].values())


def test_models_listing(client):
    r = client.get("/v1/ml/models")
    assert r.status_code == 200
    body = r.json()
    for model in ("maintenance_lstm", "demand_forecaster", "leak_autoencoder",
                  "ev_thermal_autoencoder", "fleet_gcn", "carbon_forecaster"):
        assert body[model]["loaded"] is True
        assert body[model]["champion"]["version"]
        assert body[model]["champion"]["n_params"] <= 200_000


def test_maintenance_score(client):
    r = client.post("/v1/ml/maintenance/score",
                    json={"bus_id": "H2-007", "window": _maintenance_window()})
    assert r.status_code == 200
    body = r.json()
    assert body["variant"] in ("champion", "challenger")
    assert body["model_version"]
    comps = {p["component"]: p for p in body["predictions"]}
    assert set(comps) == {"fuel_cell", "compressor", "tank_valve", "battery"}
    for p in comps.values():
        assert 0.0 <= p["risk_score"] <= 1.0
        assert p["days_to_failure"] >= 0


def test_maintenance_score_validation(client):
    r = client.post("/v1/ml/maintenance/score",
                    json={"bus_id": "H2-007", "window": [[1.0, 2.0]]})
    assert r.status_code == 422


def test_demand_forecast(client):
    base = datetime(2024, 5, 1, tzinfo=timezone.utc)
    history = [{"ts": (base + timedelta(hours=i)).isoformat(),
                "ridership": float(40 + 30 * np.sin(i / 24 * 2 * np.pi)),
                "temp_c": 12.0, "precip_mm": 0.0} for i in range(96)]
    r = client.post("/v1/ml/demand/forecast",
                    json={"route_id": "R-01", "history": history})
    assert r.status_code == 200
    body = r.json()
    assert len(body["forecast"]) == 24
    assert all(f["ridership"] >= 0 for f in body["forecast"])
    assert body["variant"] in ("champion", "challenger")


def test_leak_score(client):
    normal = [[30.0, 20.0, 6.0, 35.0, 350.0, 0.02, 0.05, 12.0]]
    leaking = [[2500.0, 900.0, 6.0, 35.0, 320.0, 0.9, 0.4, 12.0]]
    r = client.post("/v1/ml/leak/score",
                    json={"subject": "H2-023", "readings": normal + leaking})
    assert r.status_code == 200
    body = r.json()
    assert len(body["scores"]) == 2
    assert body["scores"][1] > body["scores"][0]  # leak scores higher
    assert body["is_anomaly"][1] is True
    assert body["is_anomaly"][0] is False
    assert body["domain"] == "h2"  # default domain, backward compatible


def test_leak_score_ev_thermal_domain(client):
    # [cell_temp_c, cell_voltage_v, pack_current_a, ambient_c]
    normal = [[26.0, 3.62, 40.0, 12.0]]
    runaway = [[110.0, 2.90, 350.0, 12.0]]
    r = client.post("/v1/ml/leak/score",
                    json={"subject": "EV-003", "domain": "ev_thermal",
                          "readings": normal + runaway})
    assert r.status_code == 200
    body = r.json()
    assert body["domain"] == "ev_thermal"
    assert body["model_version"]
    assert len(body["scores"]) == 2
    assert body["scores"][1] > body["scores"][0]  # thermal event scores higher
    assert body["is_anomaly"][1] is True
    assert body["is_anomaly"][0] is False


def test_leak_score_domain_validation(client):
    # ev_thermal rows are 4-wide; h2 rows are 8-wide.
    r = client.post("/v1/ml/leak/score",
                    json={"subject": "EV-003", "domain": "ev_thermal",
                          "readings": [[30.0, 20.0, 6.0, 35.0, 350.0, 0.02, 0.05, 12.0]]})
    assert r.status_code == 422
    r = client.post("/v1/ml/leak/score",
                    json={"subject": "X", "domain": "cng",
                          "readings": [[1.0, 2.0, 3.0, 4.0]]})
    assert r.status_code == 422


def test_fleet_propagate(client):
    rng = np.random.default_rng(2)
    feats = rng.random((17, 7), dtype=np.float32).tolist()
    r = client.post("/v1/ml/fleet/propagate", json={"node_features": feats})
    assert r.status_code == 200
    body = r.json()
    assert len(body["nodes"]) == 17
    assert body["nodes"][0]["delay_propagation_min"] >= 0


def test_carbon_forecast(client):
    period = [[2500.0, 90.0, 8000.0, 1.0, 12.0, 0.7, 7, 2800.0]]
    r = client.post("/v1/ml/carbon/forecast",
                    json={"subject": "fleet", "periods": period})
    assert r.status_code == 200
    body = r.json()
    assert len(body["predictions"]) == 1
    assert body["predictions"][0] > 0


def test_drift_endpoint(client):
    r = client.get("/v1/ml/drift")
    assert r.status_code == 200
    assert "maintenance_lstm" in r.json()


def test_metrics_endpoint(client):
    r = client.get("/metrics")
    assert r.status_code == 200
    assert "http_requests_total" in r.text


def test_auth_fail_closed_when_issuer_unset():
    """Without KEYCLOAK_ISSUER the JWT dependency answers 503 (fail closed)."""
    from app.main import app

    app.dependency_overrides.clear()  # the module-scoped client fixture may still be active
    with TestClient(app) as c:
        r = c.post("/v1/ml/maintenance/score",
                   json={"bus_id": "H2-007", "window": _maintenance_window()})
    assert r.status_code in (401, 503)
