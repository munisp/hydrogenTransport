//! Twin state + event types (SPEC.md §3.3, packages/events schemas).

use chrono::{DateTime, Utc};
use serde::{Deserialize, Serialize};
use uuid::Uuid;

#[derive(Debug, Clone, Deserialize)]
pub struct Envelope<T> {
    pub id: Uuid,
    #[serde(rename = "type")]
    pub kind: String,
    pub source: String,
    pub time: DateTime<Utc>,
    pub data: T,
}

#[derive(Debug, Serialize)]
pub struct OutEnvelope<'a, T: Serialize> {
    pub id: Uuid,
    #[serde(rename = "type")]
    pub kind: &'a str,
    pub source: &'a str,
    pub time: DateTime<Utc>,
    pub data: T,
}

/// `telemetry.enriched` data payload (packages/events/schemas/telemetry.enriched.json).
#[derive(Debug, Clone, Deserialize, Serialize)]
pub struct TelemetryEnriched {
    pub bus_id: Uuid,
    pub ts: DateTime<Utc>,
    pub speed_kph: f64,
    pub h2_level_pct: f64,
    pub fuel_cell_kw: f64,
    pub battery_soc_pct: f64,
    pub odometer_km: f64,
    pub lat: f64,
    pub lon: f64,
    #[serde(default)]
    pub route_id: Option<String>,
    #[serde(default)]
    pub depot_id: Option<String>,
    #[serde(default)]
    pub heading_deg: Option<f64>,
}

/// Hot per-bus twin state stored as JSON at Redis key `twin:<bus_id>`.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct TwinState {
    pub bus_id: Uuid,
    pub ts: DateTime<Utc>,
    pub speed_kph: f64,
    pub h2_level_pct: f64,
    pub fuel_cell_kw: f64,
    pub battery_soc_pct: f64,
    pub odometer_km: f64,
    pub lat: f64,
    pub lon: f64,
    pub route_id: Option<String>,
    pub depot_id: Option<String>,
    pub heading_deg: Option<f64>,
    /// Derived: moving | idle | refueling
    pub status: String,
    pub updated_at: DateTime<Utc>,
}

impl TwinState {
    pub fn from_telemetry(t: &TelemetryEnriched) -> Self {
        let status = if t.speed_kph > 2.0 {
            "moving"
        } else if t.h2_level_pct < 15.0 && t.speed_kph <= 2.0 {
            "refueling"
        } else {
            "idle"
        };
        Self {
            bus_id: t.bus_id,
            ts: t.ts,
            speed_kph: t.speed_kph,
            h2_level_pct: t.h2_level_pct,
            fuel_cell_kw: t.fuel_cell_kw,
            battery_soc_pct: t.battery_soc_pct,
            odometer_km: t.odometer_km,
            lat: t.lat,
            lon: t.lon,
            route_id: t.route_id.clone(),
            depot_id: t.depot_id.clone(),
            heading_deg: t.heading_deg,
            status: status.to_string(),
            updated_at: Utc::now(),
        }
    }
}

/// `twin.updated` event payload (packages/events/schemas/twin.updated.json).
#[derive(Debug, Serialize)]
pub struct TwinUpdated {
    pub bus_id: Uuid,
    pub ts: DateTime<Utc>,
    pub state: TwinState,
}
