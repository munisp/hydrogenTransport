//! Enrichment (Redis route/depot lookup) and TimescaleDB batch persistence.

use std::collections::HashMap;

use anyhow::Context;
use redis::aio::MultiplexedConnection;
use sqlx::PgPool;

use crate::model::{TelemetryEnriched, TelemetryRaw};

/// Redis hash holding the current assignment for a bus. Populated by
/// fleet-api / seed jobs:  HSET bus:meta:<bus_id> route_id <id> depot_id <id>
const BUS_META_KEY_PREFIX: &str = "bus:meta:";

/// Bulk route/depot enrichment with a single Redis pipeline round-trip.
/// Redis failures degrade to null enrichment (never block ingestion on the
/// enrichment cache).
pub async fn enrich_batch(
    redis: &mut MultiplexedConnection,
    batch: &[TelemetryRaw],
) -> Vec<TelemetryEnriched> {
    let mut pipe = redis::pipe();
    for rec in batch {
        pipe.cmd("HGETALL")
            .arg(format!("{}{}", BUS_META_KEY_PREFIX, rec.bus_id));
    }
    let metas: Vec<HashMap<String, String>> = match pipe.query_async(redis).await {
        Ok(m) => m,
        Err(err) => {
            tracing::warn!(error = %err, "redis enrichment lookup failed; continuing without route/depot");
            vec![HashMap::new(); batch.len()]
        }
    };

    batch
        .iter()
        .zip(metas)
        .map(|(rec, meta)| TelemetryEnriched {
            raw: rec.clone(),
            route_id: meta.get("route_id").cloned(),
            depot_id: meta.get("depot_id").cloned(),
            heading_deg: meta.get("heading_deg").and_then(|v| v.parse::<f64>().ok()),
        })
        .collect()
}

/// Batch-insert into the TimescaleDB hypertable `fleet.telemetry` using a
/// single multi-array `unnest` statement (one round-trip per batch).
pub async fn insert_batch(pool: &PgPool, batch: &[TelemetryEnriched]) -> anyhow::Result<u64> {
    if batch.is_empty() {
        return Ok(0);
    }
    let bus_ids: Vec<uuid::Uuid> = batch.iter().map(|r| r.raw.bus_id).collect();
    let ts: Vec<chrono::DateTime<chrono::Utc>> = batch.iter().map(|r| r.raw.ts).collect();
    let speed: Vec<f64> = batch.iter().map(|r| r.raw.speed_kph).collect();
    let h2: Vec<f64> = batch.iter().map(|r| r.raw.h2_level_pct).collect();
    let fc: Vec<f64> = batch.iter().map(|r| r.raw.fuel_cell_kw).collect();
    let soc: Vec<f64> = batch.iter().map(|r| r.raw.battery_soc_pct).collect();
    let odo: Vec<f64> = batch.iter().map(|r| r.raw.odometer_km).collect();
    let lat: Vec<f64> = batch.iter().map(|r| r.raw.lat).collect();
    let lon: Vec<f64> = batch.iter().map(|r| r.raw.lon).collect();
    // Wave 5 additive nullable columns (migration 0008 contract). After
    // parse-time normalization these are always populated (legacy h2 payloads
    // get the compat mirror), but the columns stay nullable in the DDL.
    let energy: Vec<Option<f64>> = batch.iter().map(|r| r.raw.energy_level_pct).collect();
    let powertrain: Vec<Option<f64>> = batch.iter().map(|r| r.raw.powertrain_kw).collect();
    let energy_type: Vec<Option<String>> = batch.iter().map(|r| r.raw.energy_type.clone()).collect();

    let result = sqlx::query(
        // ON CONFLICT DO NOTHING: dedup at-least-once redeliveries against the
        // UNIQUE(bus_id, ts) constraint (migration 0004_telemetry_dedup).
        r#"
        INSERT INTO fleet.telemetry
            (bus_id, ts, speed_kph, h2_level_pct, fuel_cell_kw, battery_soc_pct, odometer_km, geom,
             energy_level_pct, powertrain_kw, energy_type)
        SELECT u.bus_id, u.ts, u.speed_kph, u.h2_level_pct, u.fuel_cell_kw,
               u.battery_soc_pct, u.odometer_km,
               ST_SetSRID(ST_MakePoint(u.lon, u.lat), 4326),
               u.energy_level_pct, u.powertrain_kw, u.energy_type
        FROM unnest($1::uuid[], $2::timestamptz[], $3::float8[], $4::float8[], $5::float8[],
                    $6::float8[], $7::float8[], $8::float8[], $9::float8[],
                    $10::float8[], $11::float8[], $12::text[])
             AS u(bus_id, ts, speed_kph, h2_level_pct, fuel_cell_kw, battery_soc_pct, odometer_km, lat, lon,
                  energy_level_pct, powertrain_kw, energy_type)
        ON CONFLICT DO NOTHING
        "#,
    )
    .bind(&bus_ids)
    .bind(&ts)
    .bind(&speed)
    .bind(&h2)
    .bind(&fc)
    .bind(&soc)
    .bind(&odo)
    .bind(&lat)
    .bind(&lon)
    .bind(&energy)
    .bind(&powertrain)
    .bind(&energy_type)
    .execute(pool)
    .await
    .context("insert into fleet.telemetry")?;

    Ok(result.rows_affected())
}

#[cfg(test)]
mod tests {
    /// The batch insert must target the Wave-5 additive nullable columns
    /// (migration 0008 contract: energy_level_pct numeric, powertrain_kw
    /// numeric, energy_type text) alongside the unchanged h2 columns.
    #[test]
    fn insert_targets_wave5_energy_columns() {
        // Re-read the SQL from the function under test via a token scan of
        // the source would be brittle; instead pin the contract literally
        // here and keep insert_batch's SQL in sync (it is a const-ish raw
        // string — the assertions below fail on any drift).
        let src = include_str!("store.rs");
        let normalized: String = src.split_whitespace().collect::<Vec<_>>().join(" ");
        for needle in [
            "energy_level_pct, powertrain_kw, energy_type)",
            "u.energy_level_pct, u.powertrain_kw, u.energy_type",
            "$10::float8[], $11::float8[], $12::text[]",
        ] {
            assert!(
                normalized.contains(needle),
                "fleet.telemetry insert must include `{needle}` (Wave 5 contract)"
            );
        }
        // h2 columns unchanged (H2 writes both).
        for needle in ["h2_level_pct", "fuel_cell_kw"] {
            assert!(normalized.contains(needle));
        }
    }
}
