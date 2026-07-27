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
    // Wave 5 (multi-energy fleet generalization): additive optional generic
    // energy fields. Absent for legacy H2 publishers — see
    // normalize_energy_fields for the compat mapping.
    #[serde(default)]
    pub energy_level_pct: Option<f64>,
    #[serde(default)]
    pub powertrain_kw: Option<f64>,
    #[serde(default)]
    pub energy_type: Option<String>,
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

/// Dead-letter payload for `telemetry.raw.dlq`: one entry per record of a
/// batch that exhausted the TimescaleDB insert retry budget.
#[derive(Debug, Serialize)]
pub struct DlqRecord<'a> {
    pub record: &'a TelemetryEnriched,
    pub error: &'a str,
    pub attempts: u32,
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
    #[error("energy_level_pct out of range: {0}")]
    EnergyLevel(f64),
    #[error("powertrain_kw negative: {0}")]
    Powertrain(f64),
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
        if let Some(e) = self.energy_level_pct {
            if !(0.0..=100.0).contains(&e) {
                return Err(ValidationError::EnergyLevel(e));
            }
        }
        if let Some(p) = self.powertrain_kw {
            if p < 0.0 {
                return Err(ValidationError::Powertrain(p));
            }
        }
        Ok(())
    }

    /// Wave 5 compat mapping: fill the generic energy fields from the
    /// H2-specific ones when a publisher did not send them, so downstream
    /// generic consumers (and the new nullable hypertable columns) always
    /// have data:
    ///
    /// - `energy_type` absent → `'h2'` (the platform's legacy fleet);
    /// - `energy_level_pct` absent → mirror `h2_level_pct`;
    /// - `powertrain_kw` absent → mirror `fuel_cell_kw`.
    ///
    /// Fields already present (mixed/non-h2 publishers) are never
    /// overwritten.
    pub fn normalize_energy_fields(&mut self) {
        if self.energy_type.is_none() {
            self.energy_type = Some("h2".to_string());
        }
        if self.energy_level_pct.is_none() {
            self.energy_level_pct = Some(self.h2_level_pct);
        }
        if self.powertrain_kw.is_none() {
            self.powertrain_kw = Some(self.fuel_cell_kw);
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn valid_raw() -> TelemetryRaw {
        TelemetryRaw {
            bus_id: Uuid::parse_str("11111111-1111-1111-1111-111111111111").unwrap(),
            ts: Utc::now(),
            speed_kph: 42.0,
            h2_level_pct: 63.5,
            fuel_cell_kw: 55.0,
            battery_soc_pct: 81.0,
            odometer_km: 12_345.6,
            lat: 52.52,
            lon: 13.405,
            energy_level_pct: None,
            powertrain_kw: None,
            energy_type: None,
        }
    }

    #[test]
    fn validate_table() {
        // (name, mutate, expected error discriminant; None = valid)
        let cases: Vec<(&str, Box<dyn Fn(&mut TelemetryRaw)>, Option<&str>)> = vec![
            ("baseline valid", Box::new(|_| {}), None),
            ("lat max boundary", Box::new(|t| t.lat = 90.0), None),
            ("lat min boundary", Box::new(|t| t.lat = -90.0), None),
            ("lon max boundary", Box::new(|t| t.lon = 180.0), None),
            ("lon min boundary", Box::new(|t| t.lon = -180.0), None),
            ("h2 full", Box::new(|t| t.h2_level_pct = 100.0), None),
            ("h2 empty", Box::new(|t| t.h2_level_pct = 0.0), None),
            ("soc full", Box::new(|t| t.battery_soc_pct = 100.0), None),
            ("speed max plausible", Box::new(|t| t.speed_kph = 160.0), None),
            ("speed zero", Box::new(|t| t.speed_kph = 0.0), None),
            ("odometer zero", Box::new(|t| t.odometer_km = 0.0), None),
            ("fuel cell idle", Box::new(|t| t.fuel_cell_kw = 0.0), None),
            ("lat too high", Box::new(|t| t.lat = 90.0001), Some("Lat")),
            ("lat too low", Box::new(|t| t.lat = -91.0), Some("Lat")),
            ("lon too high", Box::new(|t| t.lon = 180.5), Some("Lon")),
            ("lon too low", Box::new(|t| t.lon = -181.0), Some("Lon")),
            ("h2 over 100", Box::new(|t| t.h2_level_pct = 100.1), Some("H2Level")),
            ("h2 negative", Box::new(|t| t.h2_level_pct = -0.1), Some("H2Level")),
            ("soc over 100", Box::new(|t| t.battery_soc_pct = 150.0), Some("BatterySoc")),
            ("soc negative", Box::new(|t| t.battery_soc_pct = -1.0), Some("BatterySoc")),
            ("speed implausible", Box::new(|t| t.speed_kph = 160.1), Some("Speed")),
            ("speed negative", Box::new(|t| t.speed_kph = -5.0), Some("Speed")),
            ("odometer negative", Box::new(|t| t.odometer_km = -0.1), Some("Odometer")),
            ("fuel cell negative", Box::new(|t| t.fuel_cell_kw = -0.5), Some("FuelCell")),
        ];
        for (name, mutate, want_err) in cases {
            let mut t = valid_raw();
            mutate(&mut t);
            match (t.validate(), want_err) {
                (Ok(()), None) => {}
                (Err(e), Some(want)) => {
                    let got = format!("{e:?}");
                    let variant = got.split('(').next().unwrap();
                    assert_eq!(variant, want, "case {name}: wrong error variant ({e})");
                }
                (res, want) => panic!("case {name}: got {res:?}, want err={want:?}"),
            }
        }
    }

    fn envelope_json(data: serde_json::Value) -> String {
        serde_json::json!({
            "id": "22222222-2222-2222-2222-222222222222",
            "type": "telemetry.raw",
            "source": "telemetry-simulator",
            "time": "2026-07-24T12:00:00Z",
            "data": data,
        })
        .to_string()
    }

    fn raw_json() -> serde_json::Value {
        serde_json::json!({
            "bus_id": "11111111-1111-1111-1111-111111111111",
            "ts": "2026-07-24T11:59:59Z",
            "speed_kph": 42.0,
            "h2_level_pct": 63.5,
            "fuel_cell_kw": 55.0,
            "battery_soc_pct": 81.0,
            "odometer_km": 12345.6,
            "lat": 52.52,
            "lon": 13.405,
        })
    }

    #[test]
    fn envelope_parses_schema_shaped_payload() {
        // Shape per packages/events/schemas/telemetry.raw.json inside the
        // CloudEvents-ish envelope (SPEC §3.3).
        let env: Envelope<TelemetryRaw> =
            serde_json::from_str(&envelope_json(raw_json())).expect("valid envelope must parse");
        assert_eq!(env.kind, "telemetry.raw");
        assert_eq!(env.source, "telemetry-simulator");
        assert_eq!(
            env.data.bus_id.to_string(),
            "11111111-1111-1111-1111-111111111111"
        );
        assert!((env.data.speed_kph - 42.0).abs() < f64::EPSILON);
        env.data.validate().expect("valid payload must validate");
    }

    #[test]
    fn envelope_rejects_malformed() {
        let cases: Vec<(&str, String)> = vec![
            ("not json", "{nope".to_string()),
            ("missing id", {
                let mut v: serde_json::Value =
                    serde_json::from_str(&envelope_json(raw_json())).unwrap();
                v.as_object_mut().unwrap().remove("id");
                v.to_string()
            }),
            ("bad uuid", envelope_json(raw_json()).replace(
                "22222222-2222-2222-2222-222222222222",
                "not-a-uuid",
            )),
            ("bad rfc3339 time", envelope_json(raw_json()).replace(
                "2026-07-24T12:00:00Z",
                "yesterday-ish",
            )),
            ("missing data field", {
                let mut v: serde_json::Value =
                    serde_json::from_str(&envelope_json(raw_json())).unwrap();
                v.as_object_mut().unwrap().remove("data");
                v.to_string()
            }),
            ("speed as string", envelope_json({
                let mut d = raw_json();
                d["speed_kph"] = serde_json::json!("fast");
                d
            })),
            ("data missing lon", envelope_json({
                let mut d = raw_json();
                d.as_object_mut().unwrap().remove("lon");
                d
            })),
        ];
        for (name, payload) in cases {
            assert!(
                serde_json::from_str::<Envelope<TelemetryRaw>>(&payload).is_err(),
                "case {name} must fail to parse"
            );
        }
    }

    #[test]
    fn enriched_flattens_raw_fields() {
        // telemetry.enriched must keep the raw field names at top level
        // (serde flatten) so downstream consumers see one flat object.
        let enriched = TelemetryEnriched {
            raw: valid_raw(),
            route_id: Some("R10".to_string()),
            depot_id: None,
            heading_deg: Some(270.0),
        };
        let v = serde_json::to_value(&enriched).unwrap();
        assert_eq!(v["speed_kph"], serde_json::json!(42.0));
        assert_eq!(v["route_id"], serde_json::json!("R10"));
        assert!(v.get("raw").is_none(), "raw must be flattened, not nested");
        assert_eq!(v["depot_id"], serde_json::Value::Null);
    }

    // ---- Wave 5: additive generic energy fields ----

    #[test]
    fn envelope_without_energy_fields_parses_and_normalizes_to_h2_compat() {
        // Compat path: a legacy h2-only payload (pre-Wave-5 shape) parses
        // with the new fields absent, and normalization mirrors the h2 values
        // into the generic fields so downstream consumers always have data.
        let env: Envelope<TelemetryRaw> =
            serde_json::from_str(&envelope_json(raw_json())).expect("legacy payload must parse");
        let mut data = env.data;
        assert!(data.energy_level_pct.is_none());
        assert!(data.powertrain_kw.is_none());
        assert!(data.energy_type.is_none());
        data.validate().expect("legacy payload must validate");
        data.normalize_energy_fields();
        assert_eq!(data.energy_type.as_deref(), Some("h2"));
        assert_eq!(data.energy_level_pct, Some(63.5));
        assert_eq!(data.powertrain_kw, Some(55.0));
        // h2-specific fields are untouched by the mirror.
        assert!((data.h2_level_pct - 63.5).abs() < f64::EPSILON);
        assert!((data.fuel_cell_kw - 55.0).abs() < f64::EPSILON);
    }

    #[test]
    fn envelope_with_energy_fields_parses_and_is_not_overwritten() {
        // Generic path: a battery bus sends the additive fields; they parse,
        // validate, and normalization leaves them (and the h2 fields) alone.
        let mut d = raw_json();
        d["energy_level_pct"] = serde_json::json!(47.25);
        d["powertrain_kw"] = serde_json::json!(118.0);
        d["energy_type"] = serde_json::json!("battery");
        let env: Envelope<TelemetryRaw> =
            serde_json::from_str(&envelope_json(d)).expect("mixed-fleet payload must parse");
        let mut data = env.data;
        data.validate().expect("mixed-fleet payload must validate");
        data.normalize_energy_fields();
        assert_eq!(data.energy_type.as_deref(), Some("battery"));
        assert_eq!(data.energy_level_pct, Some(47.25));
        assert_eq!(data.powertrain_kw, Some(118.0));
    }

    #[test]
    fn partial_energy_fields_are_individually_mirrored() {
        // A publisher that sends only energy_type still gets the level/power
        // mirror from the h2 fields (each field falls back independently).
        let mut d = raw_json();
        d["energy_type"] = serde_json::json!("diesel");
        let env: Envelope<TelemetryRaw> =
            serde_json::from_str(&envelope_json(d)).expect("partial payload must parse");
        let mut data = env.data;
        data.validate().expect("partial payload must validate");
        data.normalize_energy_fields();
        assert_eq!(data.energy_type.as_deref(), Some("diesel"));
        assert_eq!(data.energy_level_pct, Some(63.5));
        assert_eq!(data.powertrain_kw, Some(55.0));
    }

    #[test]
    fn implausible_energy_fields_are_rejected() {
        // The new optional fields get the same plausibility treatment as
        // their h2 counterparts when present.
        let mut t = valid_raw();
        t.energy_level_pct = Some(100.1);
        assert!(matches!(
            t.validate(),
            Err(ValidationError::EnergyLevel(_))
        ));
        let mut t = valid_raw();
        t.energy_level_pct = Some(-0.1);
        assert!(matches!(
            t.validate(),
            Err(ValidationError::EnergyLevel(_))
        ));
        let mut t = valid_raw();
        t.powertrain_kw = Some(-1.0);
        assert!(matches!(
            t.validate(),
            Err(ValidationError::Powertrain(_))
        ));
        // Boundaries are valid.
        let mut t = valid_raw();
        t.energy_level_pct = Some(100.0);
        t.powertrain_kw = Some(0.0);
        t.validate().expect("boundary values must validate");
    }

    #[test]
    fn enriched_carries_normalized_energy_fields() {
        // After the compat mirror the republished telemetry.enriched payload
        // always exposes the generic fields for downstream consumers.
        let mut raw = valid_raw();
        raw.normalize_energy_fields();
        let enriched = TelemetryEnriched {
            raw,
            route_id: Some("R10".to_string()),
            depot_id: None,
            heading_deg: Some(270.0),
        };
        let v = serde_json::to_value(&enriched).unwrap();
        assert_eq!(v["energy_type"], serde_json::json!("h2"));
        assert_eq!(v["energy_level_pct"], serde_json::json!(63.5));
        assert_eq!(v["powertrain_kw"], serde_json::json!(55.0));
        // h2 fields remain (unchanged, H2 writes both).
        assert_eq!(v["h2_level_pct"], serde_json::json!(63.5));
        assert_eq!(v["fuel_cell_kw"], serde_json::json!(55.0));
    }
}
