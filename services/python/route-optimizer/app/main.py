"""route-optimizer API (port 8091).

POST /v1/optimize/route {bus_ids?, date} -> OR-Tools VRP route + refuel plan
with H2 range constraints. Deterministic seed-data fallback when the DB has
no fleet yet. Gated on the `route-energy-optimizer` toggle (404 when off).
"""

from __future__ import annotations

import logging
from contextlib import asynccontextmanager

import asyncpg
from fastapi import Depends, FastAPI, HTTPException, Request
from toggle_client import AsyncToggleClient

from .config import settings
from .data import load_problem
from .models import BusPlan, LegOut, OptimizeRequest, OptimizeResponse
from .vrp import haversine_km, plan_refuels, range_km, solve_vrp

logging.basicConfig(level=logging.INFO, format="%(asctime)s %(levelname)s %(name)s %(message)s")
log = logging.getLogger("route-optimizer")


@asynccontextmanager
async def lifespan(app: FastAPI):
    app.state.pool = await asyncpg.create_pool(settings.database_url, min_size=1, max_size=4)
    app.state.toggles = AsyncToggleClient(settings.toggle_url)
    yield
    await app.state.toggles.close()
    await app.state.pool.close()


app = FastAPI(title="H2Fleet route-optimizer", version="0.1.0", lifespan=lifespan)


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
        "service": "route-optimizer",
        "module": settings.toggle_module,
        "enabled": await request.app.state.toggles.is_enabled(settings.toggle_module),
        "db": db_ok,
    }


async def require_enabled(request: Request) -> None:
    if not await request.app.state.toggles.is_enabled(settings.toggle_module):
        raise HTTPException(status_code=404, detail="module route-energy-optimizer is disabled")


@app.post("/v1/optimize/route", response_model=OptimizeResponse, dependencies=[Depends(require_enabled)])
async def optimize_route(req: OptimizeRequest, request: Request):
    problem, source = await load_problem(request.app.state.pool, req.bus_ids, req.date)
    if not problem.buses:
        raise HTTPException(status_code=422, detail="no buses available for optimization")
    if not problem.stops:
        raise HTTPException(status_code=422, detail="no stops to optimize")

    routes, dropped, status = await _solve_in_thread(problem)

    # Phase 2: refuel insertion against shared station inventory.
    inventory = {s.station_id: s for s in problem.stations}
    plans: list[BusPlan] = []
    for route in routes:
        refuels, h2_end, feasible, notes = plan_refuels(route, list(inventory.values()), problem.depot)
        legs = [
            LegOut(sequence=i, stop_id=s.stop_id, stop_name=s.name, cumulative_km=0.0)
            for i, s in enumerate(route.stops)
        ]
        # cumulative distances
        cum = 0.0
        cur = (route.bus.lat, route.bus.lon)
        for leg in legs:
            stop = route.stops[leg.sequence]
            cum += haversine_km(*cur, stop.lat, stop.lon)
            leg.cumulative_km = round(cum, 2)
            cur = (stop.lat, stop.lon)

        plans.append(
            BusPlan(
                bus_id=route.bus.bus_id,
                fleet_no=route.bus.fleet_no,
                feasible=feasible,
                notes=notes,
                total_route_km=round(route.km, 2),
                h2_start_kg=round(route.bus.h2_kg, 2),
                h2_end_kg=round(h2_end, 2),
                range_start_km=round(range_km(route.bus), 1),
                legs=legs,
                refuels=refuels,
            )
        )

    return OptimizeResponse(
        date=req.date,
        data_source=source,
        solver_status=status,
        unassigned_stops=dropped,
        plans=plans,
    )


async def _solve_in_thread(problem):
    """OR-Tools is CPU-bound and synchronous; keep the event loop responsive."""
    import asyncio

    return await asyncio.to_thread(solve_vrp, problem)
