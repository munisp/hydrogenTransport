"""Domain + API models for route/refuel optimization.

Multi-energy (Wave-5): buses carry an `energy_type` (h2|battery|diesel|cng,
schema contract 0008) and stations a `station_type`
(h2|ev_charger|diesel|cng|mixed). Consumption/range math is per energy_type
(kg/100km for h2+cng, kWh/100km for battery, L/100km for diesel) with the
learned per-bus rate from `fleet.fuel_consumption` (the Wave-4 learner that
fleet-api exposes at GET /v1/fuel/levels) preferred over fleet defaults.
The H2 contract is unchanged: an h2 bus with no learned rate computes
identical numbers to the pre-Wave-5 code.
"""

from __future__ import annotations

import datetime as dt

from pydantic import BaseModel, Field

# ---------------------------------------------------------------- domain

#: vehicle energy_type -> stock/consumption unit.
ENERGY_UNITS: dict[str, str] = {
    "h2": "kg",
    "battery": "kwh",
    "diesel": "liters",
    "cng": "kg",
}

#: vehicle energy_type -> preferred station_type ('mixed' serves all).
ENERGY_STATION_TYPES: dict[str, str] = {
    "h2": "h2",
    "battery": "ev_charger",
    "diesel": "diesel",
    "cng": "cng",
}


def energy_unit(energy_type: str) -> str:
    return ENERGY_UNITS.get((energy_type or "h2"), "kg")


def station_serves(station_type: str, energy_type: str) -> bool:
    """A station can replenish a bus when its type matches the bus energy_type;
    'mixed' stations serve every energy_type."""
    st = station_type or "h2"
    if st == "mixed":
        return True
    return st == ENERGY_STATION_TYPES.get(energy_type or "h2", "h2")


class Bus(BaseModel):
    bus_id: str
    fleet_no: str
    lat: float
    lon: float
    h2_capacity_kg: float
    h2_level_pct: float = 100.0
    # Wave-5 (additive; defaults keep the legacy H2 behaviour).
    energy_type: str = "h2"
    energy_capacity: float | None = Field(
        default=None, description="tank/pack capacity in energy_unit "
        "(defaults to h2_capacity_kg for h2 buses)")
    consumption_per_100km: float | None = Field(
        default=None, description="learned per-bus consumption "
        "(fleet.fuel_consumption); fleet default per energy_type when unset")
    consumption_source: str = "default"   # learned | default

    @property
    def h2_kg(self) -> float:
        return self.h2_capacity_kg * self.h2_level_pct / 100.0

    @property
    def energy_unit(self) -> str:
        return energy_unit(self.energy_type)

    @property
    def capacity_units(self) -> float:
        """Tank/pack capacity in the bus's energy unit."""
        if self.energy_type == "h2" or self.energy_capacity is None:
            return self.h2_capacity_kg
        return self.energy_capacity

    @property
    def level_units(self) -> float:
        """Energy on board in the bus's energy unit (generic h2_kg)."""
        return self.capacity_units * self.h2_level_pct / 100.0


class Station(BaseModel):
    station_id: str
    name: str
    lat: float
    lon: float
    available_kg: float
    # Wave-5 (additive): h2|ev_charger|diesel|cng|mixed ('mixed' serves all).
    station_type: str = "h2"
    available_kwh: float | None = None

    @property
    def available_units(self) -> float:
        """Stock in the station's natural unit: EV chargers meter kWh
        (available_kwh column, 0008); everything else uses available_kg
        (litres for diesel stations — same numeric stock column)."""
        if self.station_type == "ev_charger" and self.available_kwh is not None:
            return self.available_kwh
        return self.available_kg


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
    kg_taken: float  # amount taken (unit-labeled via energy_unit)
    at_stop_sequence: int
    remaining_range_km_before: float
    station_type: str = "h2"
    energy_unit: str = "kg"


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
    h2_start_kg: float  # energy on board at start (unit-labeled, legacy name)
    h2_end_kg: float    # energy on board at end (unit-labeled, legacy name)
    range_start_km: float
    legs: list[LegOut]
    refuels: list[RefuelEvent]
    # Wave-5 unit labels (additive).
    energy_type: str = "h2"
    energy_unit: str = "kg"
    consumption_per_100km: float = 0.0
    consumption_source: str = "default"  # learned | default


class OptimizeResponse(BaseModel):
    date: dt.date
    data_source: str  # "database" | "seed"
    solver_status: str
    unassigned_stops: list[str]
    plans: list[BusPlan]
