"""carbon-analytics read API (optional, port 8094).

GET  /v1/carbon/credits?period=YYYY-MM  -> issued credits from citizen.carbon_credits
POST /v1/carbon/compute {period}        -> recompute + republish for a period
Gated on the `carbon-credits` toggle: 404 when disabled (SPEC §3.2).
"""

from __future__ import annotations

import logging
from contextlib import asynccontextmanager

import asyncpg
from prometheus_fastapi_instrumentator import Instrumentator
from fastapi import Depends, FastAPI, HTTPException, Query, Request
from h2fleet_auth import KeycloakJwtVerifier
from pydantic import BaseModel, Field
from toggle_client import AsyncToggleClient

from .config import settings
from .core import compute_period, period_bounds

logging.basicConfig(level=logging.INFO, format="%(asctime)s %(levelname)s %(name)s %(message)s")

# Keycloak OIDC JWT (RS256) verifier; mutating routes require a valid Bearer
# token (SPEC §3.5). Fail-closed when KEYCLOAK_ISSUER is unset.
jwt_verifier = KeycloakJwtVerifier.from_env()


@asynccontextmanager
async def lifespan(app: FastAPI):
    app.state.pool = await asyncpg.create_pool(settings.database_url, min_size=1, max_size=4)
    app.state.toggles = AsyncToggleClient(settings.toggle_url)
    yield
    await jwt_verifier.aclose()
    await app.state.toggles.close()
    await app.state.pool.close()


app = FastAPI(title="H2Fleet carbon-analytics", version="0.1.0", lifespan=lifespan)

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
    return {
        "status": "ok" if db_ok else "degraded",
        "service": "carbon-analytics",
        "module": settings.toggle_module,
        "enabled": await request.app.state.toggles.is_enabled(settings.toggle_module),
        "db": db_ok,
    }


async def require_enabled(request: Request) -> None:
    if not await request.app.state.toggles.is_enabled(settings.toggle_module):
        raise HTTPException(status_code=404, detail="module carbon-credits is disabled")


@app.get("/v1/carbon/credits", dependencies=[Depends(require_enabled)])
async def list_credits(request: Request, period: str | None = Query(default=None)):
    if period:
        try:
            period_bounds(period)
        except ValueError as exc:
            raise HTTPException(status_code=422, detail=str(exc)) from exc
    sql = """
        SELECT id::text AS credit_id, period, kg_co2_avoided::float8, credits::float8, issued_at
        FROM citizen.carbon_credits
    """
    args: list = []
    if period:
        sql += " WHERE period = $1"
        args.append(period)
    sql += " ORDER BY period DESC"
    rows = await request.app.state.pool.fetch(sql, *args)
    return {"credits": [dict(r) for r in rows], "count": len(rows)}


class ComputeRequest(BaseModel):
    period: str = Field(..., pattern=r"^\d{4}-(0[1-9]|1[0-2])$")
    publish: bool = True


@app.post(
    "/v1/carbon/compute",
    dependencies=[Depends(require_enabled), Depends(jwt_verifier.require_auth)],
)
async def compute(req: ComputeRequest, request: Request):
    result = await compute_period(request.app.state.pool, req.period, publish=req.publish)
    return {
        "period": result.period,
        "total_km": result.total_km,
        "bus_count": result.bus_count,
        "kg_co2_avoided": result.kg_co2_avoided,
        "credits": result.credits,
        "credit_id": result.credit_id,
        "event_published": result.event_published,
        "baseline_by_energy_type": result.baseline_by_energy_type,
    }
