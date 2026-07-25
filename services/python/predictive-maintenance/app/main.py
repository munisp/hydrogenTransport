"""predictive-maintenance API (port 8090).

POST /v1/predict {bus_id} -> per-component risk scores.
Gated on the `predictive-maintenance` toggle: 404 when disabled (SPEC §3.2).
"""

from __future__ import annotations

import asyncio
import logging
from contextlib import asynccontextmanager
from datetime import datetime, timedelta, timezone

import asyncpg
from prometheus_fastapi_instrumentator import Instrumentator
from fastapi import Depends, FastAPI, HTTPException, Request
from h2fleet_auth import KeycloakJwtVerifier
from pydantic import BaseModel, Field
from toggle_client import AsyncToggleClient

from .config import settings
from .events import persist_predictions, run_consumer_loop
from .features import fetch_features, fetch_sequence
from .model import ComponentRisk, load_model

logging.basicConfig(level=logging.INFO, format="%(asctime)s %(levelname)s %(name)s %(message)s")
log = logging.getLogger("predictive-maintenance")

# Keycloak OIDC JWT (RS256) verifier; mutating routes require a valid Bearer
# token (SPEC §3.5). Fail-closed when KEYCLOAK_ISSUER is unset.
jwt_verifier = KeycloakJwtVerifier.from_env()


class PredictRequest(BaseModel):
    bus_id: str = Field(..., description="UUID of the bus")


class ComponentPrediction(BaseModel):
    component: str
    risk_score: float
    predicted_failure_at: datetime


class PredictResponse(BaseModel):
    bus_id: str
    model_version: str
    feature_window_hours: int
    predictions: list[ComponentPrediction]


@asynccontextmanager
async def lifespan(app: FastAPI):
    pool = await asyncpg.create_pool(settings.database_url, min_size=1, max_size=8)
    toggles = AsyncToggleClient(settings.toggle_url)
    model = load_model(settings.model_path, settings.model_artifacts_dir)
    app.state.pool = pool
    app.state.toggles = toggles
    app.state.model_holder = {"model": model}
    log.info("model loaded: %s", model.version)

    consumer_task = asyncio.create_task(run_consumer_loop(pool, toggles, app.state.model_holder))
    yield
    consumer_task.cancel()
    try:
        await consumer_task
    except asyncio.CancelledError:
        pass
    await jwt_verifier.aclose()
    await toggles.close()
    await pool.close()


app = FastAPI(title="H2Fleet predictive-maintenance", version="0.1.0", lifespan=lifespan)

# Prometheus metrics: GET /metrics (infra/observability/prometheus.yml, job h2fleet-services).
Instrumentator().instrument(app).expose(app, endpoint="/metrics")


@app.get("/healthz")
async def healthz(request: Request):
    db_ok = False
    try:
        await request.app.state.pool.fetchval("SELECT 1")
        db_ok = True
    except Exception:
        pass
    enabled = await request.app.state.toggles.is_enabled(settings.toggle_module)
    return {
        "status": "ok" if db_ok else "degraded",
        "service": "predictive-maintenance",
        "module": settings.toggle_module,
        "enabled": enabled,
        "model_version": request.app.state.model_holder["model"].version,
        "db": db_ok,
    }


async def require_enabled(request: Request) -> None:
    enabled = await request.app.state.toggles.is_enabled(settings.toggle_module)
    if not enabled:
        # Module OFF => domain routes 404 (SPEC §3.2).
        raise HTTPException(status_code=404, detail="module predictive-maintenance is disabled")


@app.post(
    "/v1/predict",
    response_model=PredictResponse,
    dependencies=[Depends(require_enabled), Depends(jwt_verifier.require_auth)],
)
async def predict(req: PredictRequest, request: Request):
    pool = request.app.state.pool
    model = request.app.state.model_holder["model"]
    features = await fetch_features(pool, req.bus_id, settings.feature_window_hours)
    if features is None:
        raise HTTPException(
            status_code=404, detail=f"no telemetry for bus {req.bus_id} in the last "
            f"{settings.feature_window_hours}h"
        )
    if getattr(model, "needs_sequence", False):
        seq = await fetch_sequence(pool, req.bus_id, settings.feature_window_hours)
        if seq is None:
            raise HTTPException(
                status_code=404, detail=f"insufficient telemetry rows for bus "
                f"{req.bus_id} to build the LSTM input window"
            )
        features["_sequence"] = seq
    risks: list[ComponentRisk] = model.predict_all(features)
    now = datetime.now(timezone.utc)
    try:
        await persist_predictions(pool, req.bus_id, model.version, risks)
    except Exception:
        log.exception("failed to persist predictions (continuing)")

    return PredictResponse(
        bus_id=req.bus_id,
        model_version=model.version,
        feature_window_hours=settings.feature_window_hours,
        predictions=[
            ComponentPrediction(
                component=r.component,
                risk_score=r.risk_score,
                predicted_failure_at=now + timedelta(days=r.horizon_days),
            )
            for r in risks
        ],
    )
