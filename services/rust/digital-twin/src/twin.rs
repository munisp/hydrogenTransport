//! Twin engine: consumes telemetry.enriched, maintains Redis hot state,
//! publishes twin.updated, and periodically snapshots to Postgres.

use std::time::Duration;

use anyhow::Context;
use chrono::Utc;
use rdkafka::consumer::{CommitMode, Consumer, StreamConsumer};
use rdkafka::message::Message;
use rdkafka::producer::{FutureProducer, FutureRecord};
use redis::aio::MultiplexedConnection;
use redis::AsyncCommands;
use sqlx::PgPool;
use tokio::sync::watch;
use uuid::Uuid;

use crate::config::Config;
use crate::model::{Envelope, OutEnvelope, TelemetryEnriched, TwinState, TwinUpdated};
use crate::toggles::ToggleGate;

const SERVICE_NAME: &str = "digital-twin";
pub const TWIN_KEY_PREFIX: &str = "twin:";
pub const TWIN_INDEX_KEY: &str = "twin:buses";

/// Hot-state update loop. While the module is disabled the subscription is
/// paused (heartbeats continue) and hot state is left untouched.
pub async fn run_engine(
    cfg: Config,
    redis: MultiplexedConnection,
    producer: FutureProducer,
    consumer: StreamConsumer,
    gate: ToggleGate,
    mut shutdown: watch::Receiver<bool>,
) -> anyhow::Result<()> {
    consumer
        .subscribe(&[cfg.input_topic.as_str()])
        .context("subscribe")?;
    let mut redis = redis;
    let mut paused = false;

    loop {
        if !gate.is_enabled() {
            if !paused {
                if let Ok(tpl) = consumer.assignment() {
                    let _ = consumer.pause(&tpl);
                }
                paused = true;
                tracing::info!("digital-twin disabled; consumer paused");
            }
            let _ = tokio::time::timeout(Duration::from_millis(1000), consumer.recv()).await;
            if *shutdown.borrow() {
                break;
            }
            continue;
        }
        if paused {
            if let Ok(tpl) = consumer.assignment() {
                let _ = consumer.resume(&tpl);
            }
            paused = false;
            tracing::info!("digital-twin enabled; consumer resumed");
        }

        let msg = tokio::select! {
            biased;
            _ = shutdown.changed() => break,
            m = tokio::time::timeout(Duration::from_millis(500), consumer.recv()) => m,
        };

        let km = match msg {
            Ok(Ok(km)) => km,
            Ok(Err(err)) => {
                tracing::warn!(error = %err, "kafka consume error");
                tokio::time::sleep(Duration::from_millis(500)).await;
                continue;
            }
            Err(_) => continue, // poll timeout
        };

        let Some(payload) = km.payload().map(|p| p.to_vec()) else { continue };

        match serde_json::from_slice::<Envelope<TelemetryEnriched>>(&payload) {
            Ok(env) if env.kind == cfg.input_topic => {
                tracing::trace!(event_id = %env.id, source = %env.source, event_time = %env.time, "twin update");
                if let Err(err) = apply_update(&cfg, &mut redis, &producer, &env.data).await {
                    tracing::error!(error = %err, "twin update failed; offset not committed");
                    tokio::time::sleep(Duration::from_millis(500)).await;
                    continue;
                }
            }
            Ok(_) => tracing::warn!("dropping envelope with unexpected type"),
            Err(err) => tracing::warn!(error = %err, "dropping malformed envelope"),
        }
        let _ = consumer.commit_message(&km, CommitMode::Async);
    }
    Ok(())
}

/// Apply one telemetry update to the hot twin and broadcast twin.updated.
async fn apply_update(
    cfg: &Config,
    redis: &mut MultiplexedConnection,
    producer: &FutureProducer,
    t: &TelemetryEnriched,
) -> anyhow::Result<()> {
    let key = format!("{}{}", TWIN_KEY_PREFIX, t.bus_id);
    // Load the previous hot state as the trend baseline for status
    // derivation (refueling is a TREND — h2 rising across consecutive
    // readings — not a static snapshot label). A missing/corrupt previous
    // state simply means "no baseline" (first sighting of the bus).
    let prev: Option<TwinState> = redis
        .get::<_, Option<String>>(&key)
        .await
        .ok()
        .flatten()
        .and_then(|j| serde_json::from_str(&j).ok());
    let state = TwinState::from_telemetry(prev.as_ref(), t);
    let json = serde_json::to_string(&state).context("serialize twin")?;
    let ttl = cfg.twin_ttl.as_secs() as i64;

    redis::pipe()
        .cmd("SET")
        .arg(&key)
        .arg(&json)
        .arg("EX")
        .arg(ttl)
        .ignore()
        .cmd("SADD")
        .arg(TWIN_INDEX_KEY)
        .arg(state.bus_id.to_string())
        .ignore()
        .query_async::<()>(redis)
        .await
        .context("write twin hot state")?;

    metrics::counter!("digital_twin_tracked_total").increment(1);

    let event = OutEnvelope {
        id: Uuid::new_v4(),
        kind: cfg.output_topic.as_str(),
        source: SERVICE_NAME,
        time: Utc::now(),
        data: TwinUpdated { bus_id: state.bus_id, ts: state.ts, state },
    };
    let payload = serde_json::to_vec(&event).context("serialize twin.updated")?;
    let key = t.bus_id.to_string();
    let record = FutureRecord::to(cfg.output_topic.as_str()).key(&key).payload(&payload);
    if let Err((err, _)) = producer.send(record, Duration::from_secs(5)).await {
        // Hot state is already updated; a missed notification is non-fatal.
        tracing::warn!(error = %err, "twin.updated publish failed");
    }
    Ok(())
}

/// Periodic snapshot of every hot twin into `fleet.twin_snapshots`.
pub async fn run_snapshots(
    cfg: Config,
    pool: PgPool,
    redis: MultiplexedConnection,
    gate: ToggleGate,
    mut shutdown: watch::Receiver<bool>,
) {
    let mut redis = redis;
    let mut ticker = tokio::time::interval(cfg.snapshot_interval);
    ticker.tick().await; // first tick is immediate; skip it
    loop {
        tokio::select! {
            biased;
            _ = shutdown.changed() => break,
            _ = ticker.tick() => {},
        }
        if !gate.is_enabled() {
            continue;
        }
        match snapshot_once(&pool, &mut redis).await {
            Ok(n) if n > 0 => {
                metrics::counter!("digital_twin_snapshots_total").increment(n as u64);
                tracing::info!(snapshots = n, "twin snapshots written");
            }
            Ok(_) => {},
            Err(err) => tracing::error!(error = %err, "twin snapshot failed"),
        }
    }
}

/// Split the twin index into (fresh bus ids, fresh states, stale index keys).
/// An index entry is stale when its hot-state key has TTL-expired (None), the
/// stored JSON is corrupt, or the index key itself is not a UUID — stale
/// entries are pruned from `twin:buses` and never snapshotted. Pure function
/// (extracted from snapshot_once) so the staleness rules are unit-testable
/// without a live Redis.
fn partition_states(
    bus_ids: &[String],
    values: Vec<Option<String>>,
) -> (Vec<Uuid>, Vec<serde_json::Value>, Vec<String>) {
    let mut ids: Vec<Uuid> = Vec::new();
    let mut states: Vec<serde_json::Value> = Vec::new();
    let mut stale: Vec<String> = Vec::new();
    for (bus, val) in bus_ids.iter().zip(values) {
        match val {
            Some(json) => match (Uuid::parse_str(bus), serde_json::from_str::<serde_json::Value>(&json)) {
                (Ok(id), Ok(state)) => {
                    ids.push(id);
                    states.push(state);
                }
                _ => stale.push(bus.clone()),
            },
            None => stale.push(bus.clone()), // TTL expired: prune from index
        }
    }
    (ids, states, stale)
}

async fn snapshot_once(pool: &PgPool, redis: &mut MultiplexedConnection) -> anyhow::Result<usize> {
    let bus_ids: Vec<String> = redis.smembers(TWIN_INDEX_KEY).await.context("smembers twin:buses")?;
    if bus_ids.is_empty() {
        return Ok(0);
    }
    let keys: Vec<String> = bus_ids.iter().map(|b| format!("{}{}", TWIN_KEY_PREFIX, b)).collect();
    // MGET: `redis.get(keys)` would emit `GET k1 k2 ...` (a server error), so
    // issue an explicit MGET; nil entries are TTL-expired twins (pruned below).
    let values: Vec<Option<String>> = redis::cmd("MGET")
        .arg(&keys)
        .query_async(&mut *redis)
        .await
        .context("mget twins")?;

    let (ids, states, stale) = partition_states(&bus_ids, values);
    if !stale.is_empty() {
        let _: () = redis.srem(TWIN_INDEX_KEY, stale).await.unwrap_or(());
    }
    if ids.is_empty() {
        return Ok(0);
    }

    let rows = sqlx::query(SNAPSHOT_INSERT_SQL)
    .bind(&ids)
    .bind(&states)
    .execute(pool)
    .await
    .context("insert fleet.twin_snapshots")?
    .rows_affected();
    Ok(rows as usize)
}

/// Snapshot insert. fleet.twin_snapshots is (id, bus_id, state, updated_at)
/// — there is NO `ts` column; `updated_at` defaults to now(). The previous
/// `(bus_id, ts, state)` insert failed every batch with "column ts does not
/// exist" (BUSINESS_LOGIC_AUDIT S1 / digital-twin P0). Kept as a const so a
/// regression test pins the column list against the migration DDL.
const SNAPSHOT_INSERT_SQL: &str = r#"
    INSERT INTO fleet.twin_snapshots (bus_id, state)
    SELECT u.bus_id, u.state
    FROM unnest($1::uuid[], $2::jsonb[]) AS u(bus_id, state)
"#;

#[cfg(test)]
mod tests {
    use super::*;

    const BUS_A: &str = "11111111-1111-1111-1111-111111111111";
    const BUS_B: &str = "22222222-2222-2222-2222-222222222222";
    const BUS_C: &str = "33333333-3333-3333-3333-333333333333";

    fn state_json(bus: &str) -> String {
        serde_json::json!({
            "bus_id": bus,
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
        })
        .to_string()
    }

    #[test]
    fn partition_keeps_fresh_states() {
        let (ids, states, stale) =
            partition_states(&[BUS_A.to_string()], vec![Some(state_json(BUS_A))]);
        assert_eq!(ids, vec![Uuid::parse_str(BUS_A).unwrap()]);
        assert_eq!(states.len(), 1);
        assert_eq!(states[0]["status"], serde_json::json!("moving"));
        assert!(stale.is_empty());
    }

    #[test]
    fn partition_marks_ttl_expired_as_stale() {
        // None = hot-state key gone (TTL expired): prune from the index,
        // never snapshot.
        let (ids, states, stale) = partition_states(&[BUS_A.to_string()], vec![None]);
        assert!(ids.is_empty() && states.is_empty());
        assert_eq!(stale, vec![BUS_A.to_string()]);
    }

    #[test]
    fn partition_marks_corrupt_json_as_stale() {
        let (ids, _, stale) =
            partition_states(&[BUS_A.to_string()], vec![Some("{not json".to_string())]);
        assert!(ids.is_empty());
        assert_eq!(stale, vec![BUS_A.to_string()]);
    }

    #[test]
    fn partition_marks_non_uuid_index_key_as_stale() {
        let (ids, _, stale) =
            partition_states(&["not-a-uuid".to_string()], vec![Some(state_json(BUS_A))]);
        assert!(ids.is_empty());
        assert_eq!(stale, vec!["not-a-uuid".to_string()]);
    }

    #[test]
    fn partition_mixed_batch() {
        let bus_ids = vec![
            BUS_A.to_string(),
            BUS_B.to_string(),
            BUS_C.to_string(),
            "garbage".to_string(),
        ];
        let values = vec![
            Some(state_json(BUS_A)),
            None, // TTL expired
            Some(state_json(BUS_C)),
            Some(state_json(BUS_A)), // valid JSON but non-UUID index key
        ];
        let (ids, states, stale) = partition_states(&bus_ids, values);
        assert_eq!(
            ids,
            vec![
                Uuid::parse_str(BUS_A).unwrap(),
                Uuid::parse_str(BUS_C).unwrap()
            ]
        );
        assert_eq!(states.len(), 2);
        assert_eq!(stale, vec![BUS_B.to_string(), "garbage".to_string()]);
    }

    #[test]
    fn partition_empty_index() {
        let (ids, states, stale) = partition_states(&[], vec![]);
        assert!(ids.is_empty() && states.is_empty() && stale.is_empty());
    }

    #[test]
    fn snapshot_insert_targets_existing_columns_only() {
        // Regression for the audit S1 P0: the table is
        // (id, bus_id, state, updated_at) — inserting a `ts` column failed
        // every snapshot batch. Pin the insert's column list so the bug
        // cannot be reintroduced without failing this test.
        let normalized: String = SNAPSHOT_INSERT_SQL
            .split_whitespace()
            .collect::<Vec<_>>()
            .join(" ")
            .to_lowercase();
        assert!(
            normalized.contains("insert into fleet.twin_snapshots (bus_id, state)"),
            "snapshot insert must target exactly (bus_id, state); got: {normalized}"
        );
        assert!(!normalized.contains("(bus_id, ts, state)"));
    }

    #[test]
    fn snapshot_round_trip_twin_state_through_hot_state_codec() {
        // Round-trip: telemetry.enriched -> TwinState -> Redis hot-state JSON
        // (as apply_update stores it) -> MGET value -> partition_states ->
        // the exact JSON row bound to the snapshot insert. Proves the
        // serialize/store/recover path keeps states snapshottable.
        let bus = Uuid::parse_str(BUS_A).unwrap();
        let t = TelemetryEnriched {
            bus_id: bus,
            ts: "2026-07-24T12:00:00Z".parse::<chrono::DateTime<chrono::Utc>>().unwrap(),
            speed_kph: 42.0,
            h2_level_pct: 63.5,
            fuel_cell_kw: 55.0,
            battery_soc_pct: 81.0,
            odometer_km: 12345.6,
            lat: 52.52,
            lon: 13.405,
            route_id: Some("R10".to_string()),
            depot_id: None,
            heading_deg: Some(270.0),
            energy_level_pct: None,
            powertrain_kw: None,
            energy_type: None,
        };
        let state = TwinState::from_telemetry(None, &t);
        let hot_json = serde_json::to_string(&state).unwrap(); // Redis SET twin:<bus_id>

        let (ids, states, stale) =
            partition_states(&[BUS_A.to_string()], vec![Some(hot_json.clone())]);
        assert!(stale.is_empty());
        assert_eq!(ids, vec![bus]);
        assert_eq!(states.len(), 1);

        // The snapshotted JSON row deserializes back to an equivalent state.
        let recovered: TwinState = serde_json::from_value(states[0].clone()).unwrap();
        assert_eq!(
            serde_json::to_value(&recovered).unwrap(),
            serde_json::to_value(&state).unwrap()
        );
        assert_eq!(states[0]["status"], serde_json::json!("moving"));
        assert_eq!(states[0]["route_id"], serde_json::json!("R10"));
    }
}
