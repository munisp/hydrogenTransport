//! Event envelope and telemetry payload types (SPEC.md §3.3, packages/events schemas).

use chrono::{DateTime, Utc};
use serde::{Deserialize, Serialize};
use uuid::Uuid;

/// CloudEvents-ish envelope used on every H2Fleet topic.
#[derive(Debug, Clone, Deserialize)]
pub struct Envelope<T> {
    pub id: Uuid,
    #[serde(rename = "type")]
    pub kind: String,
    pub source: String,
    pub time: DateTime<Utc>,
    pub data: T,
}

/// Outgoing (serializable) envelope for republished events.
#[derive(Debug, Serialize)]
pub struct OutEnvelope<'a, T: Serialize> {
    pub id: Uuid,
    #[serde(rename = "type")]
    pub kind: &'a str,
    pub source: &'a str,
    pub time: DateTime<Utc>,
    pub data: T,
}

/// `telemetry.raw` data payload — matches packages/events/schemas/telemetry.raw.json.
#[derive(Debug, Clone, Deserialize, Serialize)]
pub struct TelemetryRaw {
    pub bus_id: Uuid,
    pub ts: DateTime<Utc>,
    pub speed_kph: f64,
    pub h2_level_pct: f64,
    pub fuel_cell_kw: f64,
    pub battery_soc_pct: f64,
    pub odometer_km: f64,
    pub lat: f64,
    pub lon: f64,
}

/// `telemetry.enriched` data payload — raw fields + route/depot lookup.
#[derive(Debug, Clone, Serialize)]
pub struct TelemetryEnriched {
    #[serde(flatten)]
    pub raw: TelemetryRaw,
    pub route_id: Option<String>,
    pub depot_id: Option<String>,
    pub heading_deg: Option<f64>,
}

#[derive(Debug, thiserror::Error)]
pub enum ValidationError {
    #[error("latitude out of range: {0}")]
    Lat(f64),
    #[error("longitude out of range: {0}")]
    Lon(f64),
    #[error("h2_level_pct out of range: {0}")]
    H2Level(f64),
    #[error("battery_soc_pct out of range: {0}")]
    BatterySoc(f64),
    #[error("speed_kph implausible: {0}")]
    Speed(f64),
    #[error("odometer_km negative: {0}")]
    Odometer(f64),
    #[error("fuel_cell_kw negative: {0}")]
    FuelCell(f64),
}

impl TelemetryRaw {
    /// Plausibility validation. Invalid records are dropped (never written,
    /// never republished) and counted via tracing metrics.
    pub fn validate(&self) -> Result<(), ValidationError> {
        if !(-90.0..=90.0).contains(&self.lat) {
            return Err(ValidationError::Lat(self.lat));
        }
        if !(-180.0..=180.0).contains(&self.lon) {
            return Err(ValidationError::Lon(self.lon));
        }
        if !(0.0..=100.0).contains(&self.h2_level_pct) {
            return Err(ValidationError::H2Level(self.h2_level_pct));
        }
        if !(0.0..=100.0).contains(&self.battery_soc_pct) {
            return Err(ValidationError::BatterySoc(self.battery_soc_pct));
        }
        if !(0.0..=160.0).contains(&self.speed_kph) {
            return Err(ValidationError::Speed(self.speed_kph));
        }
        if self.odometer_km < 0.0 {
            return Err(ValidationError::Odometer(self.odometer_km));
        }
        if self.fuel_cell_kw < 0.0 {
            return Err(ValidationError::FuelCell(self.fuel_cell_kw));
        }
        Ok(())
    }
}
