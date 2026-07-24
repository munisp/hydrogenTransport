"""Per-bus simulation state and the movement/fuel model.

The model is deliberately simple but plausible:
  * buses assigned to a synthetic route (R1..R6) and a real depot (an
    ``infra.stations`` row) do a heading-jittered random walk around their
    seed position;
  * speed mean-reverts towards a cruise target and is clamped to 0-50 kph;
  * buses seeded with status ``depot``/``maintenance`` stay parked (speed 0)
    but still report, so the twin/leak pipelines always see all 50 buses;
  * h2_level_pct drains proportionally to distance driven and resets to
    ~full when it crosses the refuel threshold (a refuelling event).
"""
from __future__ import annotations

import math
import random
from dataclasses import dataclass, field

EARTH_M_PER_DEG_LAT = 111_320.0


@dataclass
class BusState:
    bus_id: str
    fleet_no: str
    status: str
    lat: float
    lon: float
    route_id: str
    depot_id: str
    heading_deg: float = field(default_factory=lambda: random.uniform(0, 360))
    # Home anchor (seed position); the weak pull below keeps the random walk
    # jittering around the route/depot area instead of leaving the city.
    home_lat: float = 0.0
    home_lon: float = 0.0

    def __post_init__(self) -> None:
        if not self.home_lat:
            self.home_lat = self.lat
            self.home_lon = self.lon
    speed_kph: float = 0.0
    h2_level_pct: float = field(default_factory=lambda: random.uniform(35, 100))
    fuel_cell_kw: float = 0.0
    battery_soc_pct: float = field(default_factory=lambda: random.uniform(45, 90))
    odometer_km: float = field(default_factory=lambda: random.uniform(1_000, 80_000))

    @property
    def moving(self) -> bool:
        return self.status == "active"

    def step(self, dt_seconds: float, drain_pct_per_km: float, refuel_threshold: float) -> bool:
        """Advance the simulation by dt. Returns True when a refuel happened."""
        refuelled = False
        if self.moving:
            # Heading: small jitter plus occasional larger turn (corner/stop).
            self.heading_deg = (self.heading_deg + random.uniform(-18, 18)) % 360
            if random.random() < 0.04:
                self.heading_deg = (self.heading_deg + random.choice([-90, 90])) % 360

            # Speed: mean-revert towards a 15-45 kph cruise band with stops.
            target = 0.0 if random.random() < 0.06 else random.uniform(15, 45)
            self.speed_kph += (target - self.speed_kph) * 0.35 + random.uniform(-2, 2)
            self.speed_kph = min(50.0, max(0.0, self.speed_kph))

            dist_km = self.speed_kph * dt_seconds / 3600.0
            heading_rad = math.radians(self.heading_deg)
            self.lat += (dist_km * 1000.0 * math.cos(heading_rad)) / EARTH_M_PER_DEG_LAT
            lon_scale = EARTH_M_PER_DEG_LAT * max(0.2, math.cos(math.radians(self.lat)))
            self.lon += (dist_km * 1000.0 * math.sin(heading_rad)) / lon_scale
            self.odometer_km += dist_km

            # Weak home pull (~0.5%/tick): bounded jitter around the route.
            self.lat += (self.home_lat - self.lat) * 0.005
            self.lon += (self.home_lon - self.lon) * 0.005

            # Fuel cell output tracks tractive demand; battery SOC oscillates.
            self.fuel_cell_kw = max(0.0, self.speed_kph * random.uniform(1.1, 1.7))
            self.battery_soc_pct = min(
                98.0, max(30.0, self.battery_soc_pct + random.uniform(-1.2, 1.4))
            )

            self.h2_level_pct -= dist_km * drain_pct_per_km
            if self.h2_level_pct <= refuel_threshold:
                # Refuel event at the depot: tank back to ~full.
                self.h2_level_pct = random.uniform(92, 100)
                refuelled = True
        else:
            self.speed_kph = 0.0
            self.fuel_cell_kw = random.uniform(1.5, 4.0)  # hotel loads
            self.battery_soc_pct = min(100.0, self.battery_soc_pct + 0.05 * dt_seconds)
        self.h2_level_pct = min(100.0, max(0.0, self.h2_level_pct))
        return refuelled
