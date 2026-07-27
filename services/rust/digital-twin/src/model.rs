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
    // Wave 5 (multi-energy fleet generalization): additive optional generic
    // energy fields. Absent for legacy H2 publishers — the twin then falls
    // back to the h2_* fields (fully backward compatible).
    #[serde(default)]
    pub energy_level_pct: Option<f64>,
    #[serde(default)]
    pub powertrain_kw: Option<f64>,
    #[serde(default)]
    pub energy_type: Option<String>,
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
    /// Generic energy fields (Wave 5). None for twins fed by legacy
    /// h2-only telemetry; older Redis hot states without these keys also
    /// deserialize fine (serde default).
    #[serde(default)]
    pub energy_level_pct: Option<f64>,
    #[serde(default)]
    pub powertrain_kw: Option<f64>,
    #[serde(default)]
    pub energy_type: Option<String>,
    /// Derived: moving | idle | refueling | charging | refueling_diesel |
    /// refueling_cng (see derive_status).
    pub status: String,
    pub updated_at: DateTime<Utc>,
}

/// Stationary speed threshold (kph): at or below this the bus is not moving.
const STATIONARY_KPH: f64 = 2.0;

/// Jitter tolerance for the energy-level trend (percentage points between two
/// consecutive readings). Sensor noise smaller than this is not a trend: a
/// delta within ±ENERGY_TREND_JITTER_PCT counts as "steady", not
/// rising/falling. (Same threshold as the Wave-4 H2 trend logic.)
const ENERGY_TREND_JITTER_PCT: f64 = 0.2;

/// Energy level (%) at which a tank/battery counts as full: a rising/steady
/// trend at or above this no longer means replenishment (the fill is complete).
const ENERGY_FULL_PCT: f64 = 98.0;

/// Maximum age of the previous reading for its level to still count as a
/// meaningful trend baseline. Telemetry normally arrives every few seconds;
/// a longer gap (bus offline, consumer restart) makes the delta meaningless.
const TREND_WINDOW_SECS: i64 = 120;

/// The energy-replenishment status label for an energy type (Wave 5):
/// h2 (and unknown/absent, for backward compatibility) → `refueling`,
/// battery → `charging`, diesel → `refueling_diesel`, cng → `refueling_cng`.
fn replenishing_label(energy_type: Option<&str>) -> &'static str {
    match energy_type {
        Some("battery") => "charging",
        Some("diesel") => "refueling_diesel",
        Some("cng") => "refueling_cng",
        // h2, absent, or unrecognized: keep the Wave-4 label unchanged.
        _ => "refueling",
    }
}

/// The effective energy level (%) for trend detection: the generic
/// `energy_level_pct` when present, else the H2-specific `h2_level_pct`
/// (compat path for legacy H2-only publishers).
fn effective_level(energy_level_pct: Option<f64>, h2_level_pct: f64) -> f64 {
    energy_level_pct.unwrap_or(h2_level_pct)
}

/// Derive the twin status from the telemetry TREND across consecutive
/// readings, not a static snapshot label (BUSINESS_LOGIC_AUDIT: a bus parked
/// with a low tank is not necessarily refueling — refueling is when the tank
/// is actively FILLING). Wave 5 generalizes this to energy replenishment for
/// mixed fleets (battery charging, diesel/cng refueling) with energy-type
/// aware labels; the trend thresholds are unchanged from Wave 4:
///
/// - speed above the stationary threshold → `moving` (movement wins);
/// - stationary and the energy level is RISING across consecutive readings
///   (delta beyond jitter) → replenishing (`refueling` | `charging` |
///   `refueling_diesel` | `refueling_cng`, by energy type);
/// - hysteresis: once replenishing, the bus stays in that status while the
///   level keeps rising or holds steady mid-fill (no meaningful fall) below
///   full;
/// - stationary with a falling/steady level (or full, or no usable previous
///   reading) → `idle`.
pub fn derive_status(prev: Option<&TwinState>, t: &TelemetryEnriched) -> &'static str {
    if t.speed_kph > STATIONARY_KPH {
        return "moving";
    }
    // A previous reading is a usable trend baseline only within the window.
    let prev = prev.filter(|p| {
        (t.ts - p.ts).num_seconds().abs() <= TREND_WINDOW_SECS
    });
    let label = replenishing_label(t.energy_type.as_deref());
    let level = effective_level(t.energy_level_pct, t.h2_level_pct);
    let delta = prev.map(|p| level - effective_level(p.energy_level_pct, p.h2_level_pct));
    let rising = delta.is_some_and(|d| d > ENERGY_TREND_JITTER_PCT);
    let falling = delta.is_some_and(|d| d < -ENERGY_TREND_JITTER_PCT);
    let was_replenishing = prev.is_some_and(|p| p.status == label);
    let filling = rising || (was_replenishing && !falling);
    if filling && level < ENERGY_FULL_PCT {
        label
    } else {
        "idle"
    }
}

impl TwinState {
    /// Build the hot twin state from one telemetry reading, using the
    /// previous hot state (when present) as the trend baseline for status
    /// derivation.
    pub fn from_telemetry(prev: Option<&TwinState>, t: &TelemetryEnriched) -> Self {
        let status = derive_status(prev, t);
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
            energy_level_pct: t.energy_level_pct,
            powertrain_kw: t.powertrain_kw,
            energy_type: t.energy_type.clone(),
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
            energy_level_pct: None,
            powertrain_kw: None,
            energy_type: None,
        }
    }

    /// Build a previous hot state (as loaded from Redis) with the given
    /// level/status, timestamped `age_secs` before the given reading.
    fn prev_state(t: &TelemetryEnriched, h2_level_pct: f64, status: &str, age_secs: i64) -> TwinState {
        TwinState {
            bus_id: t.bus_id,
            ts: t.ts - chrono::Duration::seconds(age_secs),
            speed_kph: 0.0,
            h2_level_pct,
            fuel_cell_kw: 55.0,
            battery_soc_pct: 81.0,
            odometer_km: 12_345.6,
            lat: 52.52,
            lon: 13.405,
            route_id: Some("R10".to_string()),
            depot_id: None,
            heading_deg: Some(270.0),
            energy_level_pct: None,
            powertrain_kw: None,
            energy_type: None,
            status: status.to_string(),
            updated_at: t.ts,
        }
    }

    #[test]
    fn first_reading_has_no_trend_baseline() {
        // Without a previous reading there is no trend: a stationary bus is
        // idle even with a low tank (a parked low-tank bus is not refueling).
        let cases = [
            (42.0, 63.5, "moving"),
            (2.1, 63.5, "moving"),  // just above the 2 kph threshold
            (10.0, 10.0, "moving"), // low H2 does not override movement
            (2.0, 63.5, "idle"),    // exactly at threshold is not moving
            (0.0, 63.5, "idle"),
            (0.0, 14.9, "idle"), // stationary + low H2 but no rising trend
            (2.0, 14.9, "idle"),
            (0.0, 15.0, "idle"),
        ];
        for (speed, h2, want) in cases {
            let state = TwinState::from_telemetry(None, &telemetry(speed, h2));
            assert_eq!(
                state.status, want,
                "speed={speed} h2={h2} must derive {want}"
            );
        }
    }

    #[test]
    fn rising_h2_trend_derives_refueling() {
        // Stationary bus, level rising across consecutive readings beyond
        // jitter → actively filling → refueling.
        let t = telemetry(0.0, 42.5);
        let prev = prev_state(&t, 41.9, "idle", 5);
        assert_eq!(TwinState::from_telemetry(Some(&prev), &t).status, "refueling");
    }

    #[test]
    fn falling_h2_trend_is_not_refueling() {
        // Stationary bus with a falling level is consuming/venting, not
        // refueling — even if the previous state was refueling.
        let t = telemetry(0.0, 41.9);
        let was_idle = prev_state(&t, 42.5, "idle", 5);
        assert_eq!(TwinState::from_telemetry(Some(&was_idle), &t).status, "idle");
        let was_refueling = prev_state(&t, 42.5, "refueling", 5);
        assert_eq!(
            TwinState::from_telemetry(Some(&was_refueling), &t).status,
            "idle"
        );
    }

    #[test]
    fn refueling_hysteresis_holds_while_fill_steady() {
        // Mid-fill the level can briefly hold steady (pump pause); while it
        // is not meaningfully falling and below full, stay refueling.
        let t = telemetry(0.0, 55.0);
        let prev = prev_state(&t, 55.1, "refueling", 5); // within jitter: steady
        assert_eq!(TwinState::from_telemetry(Some(&prev), &t).status, "refueling");
    }

    #[test]
    fn full_tank_ends_refueling() {
        // Rising/steady at or above the full mark means the fill is done.
        let t = telemetry(0.0, 98.5);
        let rising = prev_state(&t, 97.9, "refueling", 5);
        assert_eq!(TwinState::from_telemetry(Some(&rising), &t).status, "idle");
    }

    #[test]
    fn jitter_does_not_flip_status() {
        // Deltas within ±ENERGY_TREND_JITTER_PCT are sensor noise, not a trend:
        // an idle bus stays idle through sub-jitter wobble.
        let t = telemetry(0.0, 63.5);
        for prev_h2 in [63.34, 63.4, 63.6, 63.66] {
            let prev = prev_state(&t, prev_h2, "idle", 5);
            assert_eq!(
                TwinState::from_telemetry(Some(&prev), &t).status,
                "idle",
                "jitter from {prev_h2} to 63.5 must not derive refueling"
            );
        }
    }

    #[test]
    fn stale_previous_reading_is_no_trend_baseline() {
        // A previous reading older than the trend window (bus was offline)
        // must not turn a level jump into a refueling trend.
        let t = telemetry(0.0, 80.0);
        let stale = prev_state(&t, 20.0, "idle", TREND_WINDOW_SECS + 1);
        assert_eq!(TwinState::from_telemetry(Some(&stale), &t).status, "idle");
        let fresh = prev_state(&t, 20.0, "idle", TREND_WINDOW_SECS - 1);
        assert_eq!(TwinState::from_telemetry(Some(&fresh), &t).status, "refueling");
    }

    #[test]
    fn moving_overrides_refueling_trend() {
        // A rising level cannot hold a bus in refueling once it drives off.
        let t = telemetry(15.0, 42.5);
        let prev = prev_state(&t, 41.9, "refueling", 5);
        assert_eq!(TwinState::from_telemetry(Some(&prev), &t).status, "moving");
    }

    #[test]
    fn from_telemetry_copies_fields() {
        let t = telemetry(42.0, 63.5);
        let ts = t.ts;
        let state = TwinState::from_telemetry(None, &t);
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
        let state = TwinState::from_telemetry(None, &telemetry(42.0, 63.5));
        let json = serde_json::to_string(&state).unwrap();
        let v: serde_json::Value = serde_json::from_str(&json).unwrap();
        assert_eq!(v["status"], serde_json::json!("moving"));
        assert_eq!(v["bus_id"], serde_json::json!(state.bus_id.to_string()));
        let back: TwinState = serde_json::from_str(&json).unwrap();
        assert_eq!(back.status, state.status);
        assert_eq!(back.bus_id, state.bus_id);
        assert!((back.h2_level_pct - state.h2_level_pct).abs() < f64::EPSILON);
    }

    // ---- Wave 5: multi-energy replenishment labels ----

    /// Telemetry for a non-h2 bus: generic energy fields set, h2 fields still
    /// present but irrelevant to the trend (energy_level_pct wins).
    fn energy_telemetry(speed_kph: f64, energy_level_pct: f64, energy_type: &str) -> TelemetryEnriched {
        TelemetryEnriched {
            energy_level_pct: Some(energy_level_pct),
            powertrain_kw: Some(120.0),
            energy_type: Some(energy_type.to_string()),
            ..telemetry(speed_kph, 0.0)
        }
    }

    fn energy_prev_state(
        t: &TelemetryEnriched,
        energy_level_pct: f64,
        status: &str,
        age_secs: i64,
    ) -> TwinState {
        TwinState {
            energy_level_pct: Some(energy_level_pct),
            powertrain_kw: Some(120.0),
            energy_type: t.energy_type.clone(),
            ..prev_state(t, 0.0, status, age_secs)
        }
    }

    #[test]
    fn rising_energy_level_on_battery_bus_derives_charging() {
        // Stationary battery bus, energy_level_pct rising beyond jitter →
        // actively charging (energy-type-aware label, same trend logic).
        let t = energy_telemetry(0.0, 42.5, "battery");
        let prev = energy_prev_state(&t, 41.9, "idle", 5);
        let state = TwinState::from_telemetry(Some(&prev), &t);
        assert_eq!(state.status, "charging");
        assert_eq!(state.energy_type.as_deref(), Some("battery"));
        assert_eq!(state.energy_level_pct, Some(42.5));
        assert_eq!(state.powertrain_kw, Some(120.0));
    }

    #[test]
    fn rising_h2_level_on_h2_bus_still_derives_refueling() {
        // Regression: an h2-typed bus (generic fields present, mirrored) keeps
        // the Wave-4 label.
        let t = energy_telemetry(0.0, 42.5, "h2");
        let prev = energy_prev_state(&t, 41.9, "idle", 5);
        assert_eq!(TwinState::from_telemetry(Some(&prev), &t).status, "refueling");
    }

    #[test]
    fn diesel_and_cng_labels_are_energy_type_aware() {
        for (energy_type, want) in [("diesel", "refueling_diesel"), ("cng", "refueling_cng")] {
            let t = energy_telemetry(0.0, 42.5, energy_type);
            let prev = energy_prev_state(&t, 41.9, "idle", 5);
            assert_eq!(
                TwinState::from_telemetry(Some(&prev), &t).status,
                want,
                "{energy_type} rising fill must derive {want}"
            );
        }
    }

    #[test]
    fn compat_h2_only_fields_still_derive_refueling() {
        // Compat path: no generic energy fields at all (legacy publisher) —
        // the trend falls back to h2_level_pct and the label to refueling.
        let t = telemetry(0.0, 42.5);
        assert!(t.energy_level_pct.is_none() && t.energy_type.is_none());
        let prev = prev_state(&t, 41.9, "idle", 5);
        assert_eq!(TwinState::from_telemetry(Some(&prev), &t).status, "refueling");
    }

    #[test]
    fn energy_level_pct_wins_over_h2_level_pct_when_present() {
        // When both are present the generic field drives the trend: a rising
        // energy_level_pct is replenishment even if h2_level_pct is flat.
        let mut t = energy_telemetry(0.0, 42.5, "battery");
        t.h2_level_pct = 60.0;
        let mut prev = energy_prev_state(&t, 41.9, "idle", 5);
        prev.h2_level_pct = 60.0; // h2 flat across readings
        assert_eq!(TwinState::from_telemetry(Some(&prev), &t).status, "charging");
    }

    #[test]
    fn charging_hysteresis_and_full_threshold_match_refueling() {
        // Same hysteresis: mid-charge steady level holds 'charging'.
        let t = energy_telemetry(0.0, 55.0, "battery");
        let prev = energy_prev_state(&t, 55.1, "charging", 5); // within jitter: steady
        assert_eq!(TwinState::from_telemetry(Some(&prev), &t).status, "charging");
        // Same full threshold: at/above 98% the charge is done.
        let t = energy_telemetry(0.0, 98.5, "battery");
        let prev = energy_prev_state(&t, 97.9, "charging", 5);
        assert_eq!(TwinState::from_telemetry(Some(&prev), &t).status, "idle");
        // Falling level ends charging.
        let t = energy_telemetry(0.0, 41.9, "battery");
        let prev = energy_prev_state(&t, 42.5, "charging", 5);
        assert_eq!(TwinState::from_telemetry(Some(&prev), &t).status, "idle");
    }

    #[test]
    fn hot_state_without_energy_fields_deserializes() {
        // Backward compat: hot state JSON written by the pre-Wave-5 twin (no
        // energy_level_pct/powertrain_kw/energy_type keys) still loads.
        let legacy = serde_json::json!({
            "bus_id": "11111111-1111-1111-1111-111111111111",
            "ts": "2026-07-24T12:00:00Z",
            "speed_kph": 42.0,
            "h2_level_pct": 63.5,
            "fuel_cell_kw": 55.0,
            "battery_soc_pct": 81.0,
            "odometer_km": 12345.6,
            "lat": 52.52,
            "lon": 13.405,
            "route_id": "R10",
            "depot_id": null,
            "heading_deg": 270.0,
            "status": "moving",
            "updated_at": "2026-07-24T12:00:01Z"
        });
        let state: TwinState = serde_json::from_value(legacy).unwrap();
        assert_eq!(state.status, "moving");
        assert!(state.energy_level_pct.is_none());
        assert!(state.powertrain_kw.is_none());
        assert!(state.energy_type.is_none());
    }
}
