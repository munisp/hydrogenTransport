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

#[cfg(test)]
mod tests {
    use super::*;

    fn telemetry(speed_kph: f64, h2_level_pct: f64) -> TelemetryEnriched {
        TelemetryEnriched {
            bus_id: Uuid::parse_str("11111111-1111-1111-1111-111111111111").unwrap(),
            ts: Utc::now(),
            speed_kph,
            h2_level_pct,
            fuel_cell_kw: 55.0,
            battery_soc_pct: 81.0,
            odometer_km: 12_345.6,
            lat: 52.52,
            lon: 13.405,
            route_id: Some("R10".to_string()),
            depot_id: None,
            heading_deg: Some(270.0),
        }
    }

    #[test]
    fn from_telemetry_status_table() {
        // (speed_kph, h2_level_pct, expected derived status)
        let cases = [
            (42.0, 63.5, "moving"),
            (2.1, 63.5, "moving"),  // just above the 2 kph threshold
            (10.0, 10.0, "moving"), // low H2 does not override movement
            (2.0, 63.5, "idle"),    // exactly at threshold is not moving
            (0.0, 63.5, "idle"),
            (0.0, 14.9, "refueling"), // stationary + low H2
            (2.0, 14.9, "refueling"),
            (0.0, 15.0, "idle"), // 15% is NOT below the refuel threshold
        ];
        for (speed, h2, want) in cases {
            let state = TwinState::from_telemetry(&telemetry(speed, h2));
            assert_eq!(
                state.status, want,
                "speed={speed} h2={h2} must derive {want}"
            );
        }
    }

    #[test]
    fn from_telemetry_copies_fields() {
        let t = telemetry(42.0, 63.5);
        let ts = t.ts;
        let state = TwinState::from_telemetry(&t);
        assert_eq!(state.bus_id, t.bus_id);
        assert_eq!(state.ts, ts);
        assert!((state.speed_kph - 42.0).abs() < f64::EPSILON);
        assert_eq!(state.route_id.as_deref(), Some("R10"));
        assert_eq!(state.depot_id, None);
        // updated_at is the apply time (≈ now), not the telemetry timestamp.
        assert!(state.updated_at >= ts);
    }

    #[test]
    fn twin_state_json_roundtrip_preserves_hot_state_shape() {
        // apply_update persists this exact JSON at twin:<bus_id>; the read API
        // deserializes it back, so the roundtrip must be lossless.
        let state = TwinState::from_telemetry(&telemetry(42.0, 63.5));
        let json = serde_json::to_string(&state).unwrap();
        let v: serde_json::Value = serde_json::from_str(&json).unwrap();
        assert_eq!(v["status"], serde_json::json!("moving"));
        assert_eq!(v["bus_id"], serde_json::json!(state.bus_id.to_string()));
        let back: TwinState = serde_json::from_str(&json).unwrap();
        assert_eq!(back.status, state.status);
        assert_eq!(back.bus_id, state.bus_id);
        assert!((back.h2_level_pct - state.h2_level_pct).abs() < f64::EPSILON);
    }
}
