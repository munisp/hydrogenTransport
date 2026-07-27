"""ocpp-gateway service entrypoint (port 8100).

* WebSocket CSMS endpoint:  /ocpp/{charge_point_id}  (OCPP 1.6J, subprotocol
  "ocpp1.6") — charge points connect here.
* JWT-gated REST read API:  /v1/ocpp/charge-points[/{ocpp_id}], /v1/ocpp/sessions
* Ops:                      /healthz (db + websocket truthfully), /metrics
"""

from __future__ import annotations

import datetime as dt
import logging

import asyncpg
from fastapi import Depends, FastAPI, HTTPException, Query, Request, WebSocket, WebSocketDisconnect
from h2fleet_auth import KeycloakJwtVerifier
from prometheus_fastapi_instrumentator import Instrumentator

from . import db
from .config import settings
from .csms import ChargePointHandler
from .events import EventPublisher

logging.basicConfig(level=logging.INFO, format="%(asctime)s %(levelname)s %(name)s %(message)s")
log = logging.getLogger("ocpp-gateway")

SERVICE_NAME = "ocpp-gateway"

# Keycloak OIDC JWT (RS256) verifier (SPEC §3.5); fail-closed when
# KEYCLOAK_ISSUER is unset. Module-level so tests can override the dependency.
jwt_verifier = KeycloakJwtVerifier.from_env()


async def require_auth(request: Request) -> dict:
    """FastAPI dependency wrapper (single override point for tests)."""
    return await jwt_verifier.require_auth(request)


from contextlib import asynccontextmanager


@asynccontextmanager
async def lifespan(app: FastAPI):
    if settings.open_charging:
        log.warning(
            "OCPP_OPEN_ID_TAGS='*' — OPEN CHARGING: every id_tag is accepted. "
            "DEV ONLY; set an explicit whitelist in production."
        )
    app.state.pool = await asyncpg.create_pool(settings.database_url, min_size=1, max_size=4)
    app.state.publisher = EventPublisher(settings.kafka_brokers, settings.status_topic)
    app.state.active_charge_points = {}
    yield
    await app.state.publisher.aclose()
    await jwt_verifier.aclose()
    await app.state.pool.close()


app = FastAPI(title="H2Fleet ocpp-gateway", version="0.1.0", lifespan=lifespan)

# Prometheus metrics: GET /metrics (infra/observability/prometheus.yml, job h2fleet-services).
Instrumentator().instrument(app).expose(app, endpoint="/metrics")


# --------------------------------------------------------------------- health
@app.get("/healthz")
async def healthz(request: Request):
    db_ok = False
    try:
        await request.app.state.pool.fetchval("SELECT 1")
        db_ok = True
    except Exception:
        pass
    publisher = getattr(request.app.state, "publisher", None)
    active = getattr(request.app.state, "active_charge_points", {})
    return {
        "status": "ok" if db_ok else "degraded",
        "service": SERVICE_NAME,
        "db": db_ok,
        "websocket": {
            "path": "/ocpp/{charge_point_id}",
            "subprotocol": "ocpp1.6",
            "active_charge_points": len(active),
        },
        "kafka_connected": bool(publisher and publisher.connected),
        "open_charging": settings.open_charging,
    }


# ------------------------------------------------------------ OCPP WebSocket
class _WebSocketAdapter:
    """Adapt a starlette WebSocket to the recv/send interface the `ocpp`
    library expects from a `websockets` connection."""

    def __init__(self, websocket: WebSocket) -> None:
        self._ws = websocket

    async def recv(self) -> str:
        return await self._ws.receive_text()

    async def send(self, message: str) -> None:
        await self._ws.send_text(message)

    async def close(self, code: int = 1000) -> None:
        await self._ws.close(code=code)


@app.websocket("/ocpp/{charge_point_id}")
async def ocpp_websocket(websocket: WebSocket, charge_point_id: str):
    offered = websocket.headers.get("sec-websocket-protocol", "")
    subprotocol = "ocpp1.6" if "ocpp1.6" in offered else None
    if subprotocol is None:
        log.warning("charge point %s connected without ocpp1.6 subprotocol", charge_point_id)
    await websocket.accept(subprotocol=subprotocol)

    handler = ChargePointHandler(
        charge_point_id,
        _WebSocketAdapter(websocket),
        pool=websocket.app.state.pool,
        publisher=websocket.app.state.publisher,
        settings=settings,
    )
    websocket.app.state.active_charge_points[charge_point_id] = dt.datetime.now(dt.timezone.utc)
    log.info("charge point connected: %s", charge_point_id)
    try:
        await handler.start()
    except WebSocketDisconnect:
        pass
    except Exception:
        log.exception("charge point %s connection error", charge_point_id)
    finally:
        websocket.app.state.active_charge_points.pop(charge_point_id, None)
        log.info("charge point disconnected: %s", charge_point_id)


# ------------------------------------------------------------- REST read API
@app.get("/v1/ocpp/charge-points", dependencies=[Depends(require_auth)])
async def list_charge_points(request: Request):
    points = await db.list_charge_points(request.app.state.pool)
    return {"charge_points": points, "count": len(points)}


@app.get("/v1/ocpp/charge-points/{ocpp_id}", dependencies=[Depends(require_auth)])
async def get_charge_point(ocpp_id: str, request: Request):
    point = await db.get_charge_point(request.app.state.pool, ocpp_id)
    if point is None:
        raise HTTPException(status_code=404, detail=f"unknown charge point {ocpp_id!r}")
    return point


@app.get("/v1/ocpp/sessions", dependencies=[Depends(require_auth)])
async def list_sessions(
    request: Request,
    station_id: str | None = Query(default=None),
    status: str | None = Query(default=None),
    limit: int = Query(default=100, ge=1, le=1000),
):
    sessions = await db.list_sessions(request.app.state.pool, station_id, status, limit)
    return {"sessions": sessions, "count": len(sessions)}
