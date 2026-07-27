"""Persistence helpers for infra.charge_points / infra.charging_sessions.

Codes against the wave-5 schema contract (plan-wave5.md, migration 0008)
verbatim:

    infra.charge_points(id uuid pk, station_id uuid fk, ocpp_id text unique
        not null, vendor text, model text, status text not null default
        'Unavailable', last_heartbeat timestamptz, created_at timestamptz
        default now())
    infra.charging_sessions(id uuid pk, charge_point_id uuid fk, bus_id text
        null, connector_id int not null, id_tag text null, meter_start numeric
        not null, meter_stop numeric null, kwh numeric null, started_at
        timestamptz not null, stopped_at timestamptz null, status text not
        null default 'active')
"""

from __future__ import annotations

import logging
from typing import Any

log = logging.getLogger("ocpp-gateway.db")


async def upsert_charge_point(pool, ocpp_id: str, vendor: str, model: str) -> dict | None:
    """BootNotification: insert or refresh the charge point row.

    A (re)boot brings the charger back to 'Available' and stamps
    last_heartbeat=now (per mission contract). station_id is preserved on
    conflict — it is assigned out-of-band (infra-api), never by OCPP.
    """
    row = await pool.fetchrow(
        """
        INSERT INTO infra.charge_points (ocpp_id, vendor, model, status, last_heartbeat)
        VALUES ($1, $2, $3, 'Available', now())
        ON CONFLICT (ocpp_id) DO UPDATE SET
            vendor         = EXCLUDED.vendor,
            model          = EXCLUDED.model,
            status         = 'Available',
            last_heartbeat = now()
        RETURNING id::text AS id, station_id::text AS station_id
        """,
        ocpp_id,
        vendor,
        model,
    )
    return dict(row) if row else None


async def touch_heartbeat(pool, ocpp_id: str) -> None:
    await pool.execute(
        "UPDATE infra.charge_points SET last_heartbeat = now() WHERE ocpp_id = $1",
        ocpp_id,
    )


async def set_status(pool, ocpp_id: str, status: str) -> dict | None:
    """StatusNotification: persist the raw OCPP status; return the row plus
    the linked station's available_kwh (contract additive event field, when
    known)."""
    row = await pool.fetchrow(
        """
        UPDATE infra.charge_points SET status = $2
        WHERE ocpp_id = $1
        RETURNING id::text AS id, station_id::text AS station_id,
                  (SELECT s.available_kwh::float8 FROM infra.stations s
                    WHERE s.id = charge_points.station_id) AS available_kwh
        """,
        ocpp_id,
        status,
    )
    return dict(row) if row else None


async def get_charge_point_pk(pool, ocpp_id: str) -> str | None:
    return await pool.fetchval(
        "SELECT id::text FROM infra.charge_points WHERE ocpp_id = $1", ocpp_id
    )


async def find_bus_id(pool, id_tag: str) -> str | None:
    """Resolve an id_tag to a known bus (fleet.vehicles uuid or fleet_no)."""
    return await pool.fetchval(
        """
        SELECT id::text FROM fleet.vehicles
        WHERE id::text = $1 OR fleet_no = $1
        ORDER BY (id::text = $1) DESC
        LIMIT 1
        """,
        id_tag,
    )


async def start_session(
    pool,
    charge_point_id: str,
    connector_id: int,
    id_tag: str | None,
    bus_id: str | None,
    meter_start: float,
) -> str | None:
    row = await pool.fetchrow(
        """
        INSERT INTO infra.charging_sessions
            (charge_point_id, bus_id, connector_id, id_tag, meter_start, started_at, status)
        VALUES ($1::uuid, $2, $3, $4, $5, now(), 'active')
        RETURNING id::text AS id
        """,
        charge_point_id,
        bus_id,
        connector_id,
        id_tag,
        meter_start,
    )
    return row["id"] if row else None


async def update_session_kwh(pool, session_id: str, register_value: float, kwh_factor: float) -> None:
    """MeterValues: running energy delivered, in kWh.

    kwh = (register - meter_start) * kwh_factor, computed in SQL so the
    meter_start authority stays in the row.
    """
    await pool.execute(
        """
        UPDATE infra.charging_sessions
        SET kwh = ($2 - meter_start) * $3
        WHERE id = $1::uuid AND status = 'active'
        """,
        session_id,
        register_value,
        kwh_factor,
    )


async def stop_session(pool, session_id: str, meter_stop: float, kwh_factor: float) -> dict | None:
    row = await pool.fetchrow(
        """
        UPDATE infra.charging_sessions
        SET meter_stop = $2,
            kwh        = ($2 - meter_start) * $3,
            stopped_at = now(),
            status     = 'completed'
        WHERE id = $1::uuid AND status = 'active'
        RETURNING id::text AS id, kwh::float8 AS kwh
        """,
        session_id,
        meter_stop,
        kwh_factor,
    )
    return dict(row) if row else None


# --------------------------------------------------------------------- reads
_CHARGE_POINT_COLS = """
    id::text AS id, station_id::text AS station_id, ocpp_id, vendor, model,
    status, last_heartbeat, created_at
"""

_SESSION_COLS = """
    s.id::text AS id, s.charge_point_id::text AS charge_point_id, cp.ocpp_id,
    cp.station_id::text AS station_id, s.bus_id, s.connector_id, s.id_tag,
    s.meter_start::float8 AS meter_start, s.meter_stop::float8 AS meter_stop,
    s.kwh::float8 AS kwh, s.started_at, s.stopped_at, s.status
"""


async def list_charge_points(pool) -> list[dict[str, Any]]:
    rows = await pool.fetch(
        f"SELECT {_CHARGE_POINT_COLS} FROM infra.charge_points ORDER BY ocpp_id"
    )
    return [dict(r) for r in rows]


async def get_charge_point(pool, ocpp_id: str) -> dict | None:
    row = await pool.fetchrow(
        f"SELECT {_CHARGE_POINT_COLS} FROM infra.charge_points WHERE ocpp_id = $1",
        ocpp_id,
    )
    return dict(row) if row else None


async def list_sessions(
    pool, station_id: str | None, status: str | None, limit: int
) -> list[dict[str, Any]]:
    rows = await pool.fetch(
        f"""
        SELECT {_SESSION_COLS}
        FROM infra.charging_sessions s
        JOIN infra.charge_points cp ON cp.id = s.charge_point_id
        WHERE ($1::text IS NULL OR cp.station_id::text = $1)
          AND ($2::text IS NULL OR s.status = $2)
        ORDER BY s.started_at DESC
        LIMIT $3
        """,
        station_id,
        status,
        limit,
    )
    return [dict(r) for r in rows]
