"""H2Fleet ml-platform inference server (port 8095).

Serves the six trained models (maintenance LSTM, demand forecaster, leak
autoencoder, ev_thermal autoencoder, fleet GCN, carbon forecaster) on CPU with:

* champion/challenger A/B loading (registry.json + AB_SPLIT, deterministic
  per-subject assignment, variant-tagged responses and logs)
* a background drift monitor (PSI/KS vs training baselines) -> GET /v1/ml/drift
  + warning logs + `ml_feature_drift_psi` Prometheus gauge
* Keycloak RS256 JWT on all scoring routes (shared h2fleet_auth package)
* /healthz and /metrics (prometheus-fastapi-instrumentator)
"""

from __future__ import annotations

import asyncio
import logging
from contextlib import asynccontextmanager

import numpy as np
from fastapi import Depends, FastAPI, HTTPException, Request
from h2fleet_auth import KeycloakJwtVerifier
from prometheus_client import Gauge
from prometheus_fastapi_instrumentator import Instrumentator

from .config import settings
from .schemas import (CarbonForecastRequest, DemandForecastRequest,
                      FleetPropagateRequest, LeakScoreRequest,
                      MaintenanceScoreRequest)
from .serving import ModelServer, ModelUnavailable

logging.basicConfig(level=logging.INFO, format="%(asctime)s %(levelname)s %(name)s %(message)s")
log = logging.getLogger("ml-platform")

jwt_verifier = KeycloakJwtVerifier.from_env()

DRIFT_GAUGE = Gauge("ml_feature_drift_psi", "Feature drift PSI vs training baseline",
                    ["model", "feature"])


@asynccontextmanager
async def lifespan(app: FastAPI):
    server = ModelServer(settings.model_artifacts_dir, ab_split=settings.ab_split,
                         drift_window=settings.drift_window,
                         drift_psi_warn=settings.drift_psi_warn)
    app.state.server = server

    async def drift_loop():
        while True:
            await asyncio.sleep(settings.drift_interval_s)
            try:
                report = await asyncio.to_thread(server.recompute_drift)
                for model, snap in report.items():
                    for f, r in snap.get("features", {}).items():
                        DRIFT_GAUGE.labels(model=model, feature=f).set(r["psi"])
            except Exception:
                log.exception("drift recompute failed")

    task = asyncio.create_task(drift_loop())
    log.info("ml-platform ready (artifacts=%s, ab_split=%.3f)",
             settings.model_artifacts_dir, settings.ab_split)
    yield
    task.cancel()
    try:
        await task
    except asyncio.CancelledError:
        pass
    await jwt_verifier.aclose()


app = FastAPI(title="H2Fleet ml-platform", version="1.0.0", lifespan=lifespan)
Instrumentator().instrument(app).expose(app, endpoint="/metrics")


@app.exception_handler(ModelUnavailable)
async def model_unavailable_handler(_: Request, exc: ModelUnavailable):
    from fastapi.responses import JSONResponse
    return JSONResponse(status_code=503, content={"detail": str(exc)})


@app.get("/healthz")
async def healthz(request: Request):
    server: ModelServer = request.app.state.server
    return {"status": "ok", "service": "ml-platform",
            "models_loaded": {m: bool(b) for m, b in server.models.items()},
            "ab_split": server.ab_split}


@app.get("/v1/ml/models")
async def list_models(request: Request):
    return request.app.state.server.info()


@app.get("/v1/ml/drift")
async def drift(request: Request):
    return request.app.state.server.drift_snapshot()


@app.post("/v1/ml/maintenance/score",
          dependencies=[Depends(jwt_verifier.require_auth)])
async def maintenance_score(req: MaintenanceScoreRequest, request: Request):
    result = await asyncio.to_thread(
        request.app.state.server.maintenance_score, req.bus_id, req.window)
    log.info("maintenance/score bus=%s variant=%s version=%s",
             req.bus_id, result["variant"], result["model_version"])
    return result


@app.post("/v1/ml/demand/forecast",
          dependencies=[Depends(jwt_verifier.require_auth)])
async def demand_forecast(req: DemandForecastRequest, request: Request):
    result = await asyncio.to_thread(
        request.app.state.server.demand_forecast, req.route_id, req.feature_rows())
    log.info("demand/forecast route=%s variant=%s", req.route_id, result["variant"])
    return result


@app.post("/v1/ml/leak/score",
          dependencies=[Depends(jwt_verifier.require_auth)])
async def leak_score(req: LeakScoreRequest, request: Request):
    result = await asyncio.to_thread(
        request.app.state.server.leak_score, req.subject,
        np.asarray(req.readings), req.domain)
    if any(result["is_anomaly"]):
        log.warning("%s anomaly subject=%s max_score=%.4f variant=%s",
                    req.domain, req.subject, result["max_score"], result["variant"])
    return result


@app.post("/v1/ml/fleet/propagate",
          dependencies=[Depends(jwt_verifier.require_auth)])
async def fleet_propagate(req: FleetPropagateRequest, request: Request):
    adj = np.asarray(req.adjacency, dtype=np.float32) if req.adjacency else None
    result = await asyncio.to_thread(
        request.app.state.server.fleet_propagate, np.asarray(req.node_features), adj)
    return result


@app.post("/v1/ml/carbon/forecast",
          dependencies=[Depends(jwt_verifier.require_auth)])
async def carbon_forecast(req: CarbonForecastRequest, request: Request):
    result = await asyncio.to_thread(
        request.app.state.server.carbon_forecast, req.subject, np.asarray(req.periods))
    return result
