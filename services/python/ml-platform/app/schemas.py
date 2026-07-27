"""pydantic v2 request/response contracts for the ml-platform API."""

from __future__ import annotations

from datetime import datetime

import numpy as np
from pydantic import BaseModel, Field, field_validator, model_validator

from models import (ANOMALY_DOMAIN_FEATURES, CARBON_FEATURES, DEMAND_FEATURES,
                    GRAPH_NODE_FEATURES, LEAK_SENSOR_FEATURES, SEQ_FEATURES)


class MaintenanceScoreRequest(BaseModel):
    bus_id: str = Field(..., description="fleet_no (H2-001..) or UUID")
    window: list[list[float]] = Field(
        ..., description="telemetry window rows, newest last; each row: " + ", ".join(SEQ_FEATURES))

    @field_validator("window")
    @classmethod
    def _check(cls, w):
        if not 8 <= len(w) <= 2048:
            raise ValueError("window must contain between 8 and 2048 timesteps")
        for row in w:
            if len(row) != len(SEQ_FEATURES):
                raise ValueError(f"each window row must have {len(SEQ_FEATURES)} features "
                                 f"({SEQ_FEATURES})")
        return w


class DemandHistoryPoint(BaseModel):
    ts: datetime
    ridership: float = Field(..., ge=0)
    temp_c: float = 10.0
    precip_mm: float = 0.0


class DemandForecastRequest(BaseModel):
    route_id: str
    history: list[DemandHistoryPoint] = Field(..., min_length=72, max_length=24 * 31)

    def feature_rows(self) -> np.ndarray:
        rows = []
        for p in sorted(self.history, key=lambda p: p.ts):
            hour = p.ts.hour + p.ts.minute / 60.0
            dow = float(p.ts.weekday())
            rows.append([
                p.ridership,
                float(np.sin(2 * np.pi * hour / 24)), float(np.cos(2 * np.pi * hour / 24)),
                float(np.sin(2 * np.pi * dow / 7)), float(np.cos(2 * np.pi * dow / 7)),
                1.0 if dow >= 5 else 0.0,
                p.temp_c, p.precip_mm,
            ])
        arr = np.asarray(rows, dtype=np.float32)
        return arr[-72:]  # model consumes the most recent window

    @field_validator("history")
    @classmethod
    def _check(cls, h):
        if len(h) < 72:
            raise ValueError("need at least 72 hourly history points")
        return h


class LeakScoreRequest(BaseModel):
    """Anomaly-domain scoring. domain='h2' (default) is the original h2_ppm
    leak path; domain='ev_thermal' scores battery-pack telemetry for
    thermal-runaway precursors (backward compatible: omitting `domain` keeps
    the h2 contract unchanged)."""

    subject: str = Field("fleet", description="bus fleet_no / station name for A/B + logs")
    domain: str = Field("h2", description="anomaly domain: h2 | ev_thermal")
    readings: list[list[float]] = Field(..., min_length=1, max_length=512)

    @field_validator("domain")
    @classmethod
    def _check_domain(cls, d):
        if d not in ANOMALY_DOMAIN_FEATURES:
            raise ValueError(f"unknown anomaly domain {d!r} "
                             f"(expected one of {sorted(ANOMALY_DOMAIN_FEATURES)})")
        return d

    @model_validator(mode="after")
    def _check(self):
        features = ANOMALY_DOMAIN_FEATURES[self.domain]
        for row in self.readings:
            if len(row) != len(features):
                raise ValueError(f"each reading must have {len(features)} values "
                                 f"for domain {self.domain!r} ({features})")
        return self


class FleetPropagateRequest(BaseModel):
    node_features: list[list[float]] = Field(..., min_length=2, max_length=256)
    adjacency: list[list[float]] | None = Field(
        None, description="N x N adjacency; defaults to the trained fleet graph")

    @field_validator("node_features")
    @classmethod
    def _check(cls, nf):
        for row in nf:
            if len(row) != len(GRAPH_NODE_FEATURES):
                raise ValueError(f"each node needs {len(GRAPH_NODE_FEATURES)} features "
                                 f"({GRAPH_NODE_FEATURES})")
        return nf


class CarbonForecastRequest(BaseModel):
    subject: str = "fleet"
    periods: list[list[float]] = Field(..., min_length=1, max_length=512)

    @field_validator("periods")
    @classmethod
    def _check(cls, p):
        for row in p:
            if len(row) != len(CARBON_FEATURES):
                raise ValueError(f"each period must have {len(CARBON_FEATURES)} features "
                                 f"({CARBON_FEATURES})")
        return p
