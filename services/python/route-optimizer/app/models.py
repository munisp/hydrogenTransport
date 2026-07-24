"""Domain + API models for route/refuel optimization."""

from __future__ import annotations

import datetime as dt

from pydantic import BaseModel, Field

# ---------------------------------------------------------------- domain


class Bus(BaseModel):
    bus_id: str
    fleet_no: str
    lat: float
    lon: float
    h2_capacity_kg: float
    h2_level_pct: float = 100.0

    @property
    def h2_kg(self) -> float:
        return self.h2_capacity_kg * self.h2_level_pct / 100.0


class Station(BaseModel):
    station_id: str
    name: str
    lat: float
    lon: float
    available_kg: float


class Stop(BaseModel):
    stop_id: str
    name: str
    lat: float
    lon: float


class Problem(BaseModel):
    depot: Stop
    buses: list[Bus]
    stations: list[Station]
    stops: list[Stop]


# ---------------------------------------------------------------- API


class OptimizeRequest(BaseModel):
    bus_ids: list[str] | None = Field(
        default=None, description="Subset of fleet; None = all buses with telemetry"
    )
    date: dt.date = Field(default_factory=dt.date.today)


class RefuelEvent(BaseModel):
    station_id: str
    station_name: str
    kg_taken: float
    at_stop_sequence: int
    remaining_range_km_before: float


class LegOut(BaseModel):
    sequence: int
    stop_id: str
    stop_name: str
    cumulative_km: float


class BusPlan(BaseModel):
    bus_id: str
    fleet_no: str
    feasible: bool
    notes: list[str] = []
    total_route_km: float
    h2_start_kg: float
    h2_end_kg: float
    range_start_km: float
    legs: list[LegOut]
    refuels: list[RefuelEvent]


class OptimizeResponse(BaseModel):
    date: dt.date
    data_source: str  # "database" | "seed"
    solver_status: str
    unassigned_stops: list[str]
    plans: list[BusPlan]
