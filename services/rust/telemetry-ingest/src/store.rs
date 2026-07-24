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

    let result = sqlx::query(
        // ON CONFLICT DO NOTHING: dedup at-least-once redeliveries against the
        // UNIQUE(bus_id, ts) constraint (migration 0004_telemetry_dedup).
        r#"
        INSERT INTO fleet.telemetry
            (bus_id, ts, speed_kph, h2_level_pct, fuel_cell_kw, battery_soc_pct, odometer_km, geom)
        SELECT u.bus_id, u.ts, u.speed_kph, u.h2_level_pct, u.fuel_cell_kw,
               u.battery_soc_pct, u.odometer_km,
               ST_SetSRID(ST_MakePoint(u.lon, u.lat), 4326)
        FROM unnest($1::uuid[], $2::timestamptz[], $3::float8[], $4::float8[], $5::float8[],
                    $6::float8[], $7::float8[], $8::float8[], $9::float8[])
             AS u(bus_id, ts, speed_kph, h2_level_pct, fuel_cell_kw, battery_soc_pct, odometer_km, lat, lon)
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
    .execute(pool)
    .await
    .context("insert into fleet.telemetry")?;

    Ok(result.rows_affected())
}
