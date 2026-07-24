//! Read API: GET /v1/twin/{bus_id}, GET /v1/twin, GET /healthz.
//! When the `digital-twin` module toggle is off, API routes return 404 (SPEC §3.2).

use std::sync::Arc;

use axum::extract::{Path, State};
use axum::http::StatusCode;
use axum::response::Json;
use axum::routing::get;
use axum::Router;
use redis::aio::MultiplexedConnection;
use redis::AsyncCommands;
use uuid::Uuid;

use crate::model::TwinState;
use crate::toggles::ToggleGate;
use crate::twin::{TWIN_INDEX_KEY, TWIN_KEY_PREFIX};

#[derive(Clone)]
pub struct AppState {
    pub redis: MultiplexedConnection,
    pub gate: ToggleGate,
}

pub fn router(state: Arc<AppState>) -> Router {
    Router::new()
        .route("/healthz", get(healthz))
        .route("/v1/twin", get(list_twins))
        .route("/v1/twin/:bus_id", get(get_twin))
        .with_state(state)
}

async fn healthz(State(state): State<Arc<AppState>>) -> Json<serde_json::Value> {
    let mut redis = state.redis.clone();
    let redis_ok = redis::cmd("PING")
        .query_async::<String>(&mut redis)
        .await
        .is_ok();
    Json(serde_json::json!({
        "status": if redis_ok { "ok" } else { "degraded" },
        "service": "digital-twin",
        "module": "digital-twin",
        "enabled": state.gate.is_enabled(),
        "redis": redis_ok,
    }))
}

async fn get_twin(
    State(state): State<Arc<AppState>>,
    Path(bus_id): Path<Uuid>,
) -> Result<Json<TwinState>, StatusCode> {
    if !state.gate.is_enabled() {
        return Err(StatusCode::NOT_FOUND); // module off => routes 404 (SPEC §3.2)
    }
    let mut redis = state.redis.clone();
    let key = format!("{}{}", TWIN_KEY_PREFIX, bus_id);
    let raw: Option<String> = redis.get(key).await.map_err(|_| StatusCode::BAD_GATEWAY)?;
    match raw {
        Some(json) => serde_json::from_str::<TwinState>(&json)
            .map(Json)
            .map_err(|_| StatusCode::INTERNAL_SERVER_ERROR),
        None => Err(StatusCode::NOT_FOUND),
    }
}

async fn list_twins(
    State(state): State<Arc<AppState>>,
) -> Result<Json<serde_json::Value>, StatusCode> {
    if !state.gate.is_enabled() {
        return Err(StatusCode::NOT_FOUND);
    }
    let mut redis = state.redis.clone();
    let bus_ids: Vec<String> = redis
        .smembers(TWIN_INDEX_KEY)
        .await
        .map_err(|_| StatusCode::BAD_GATEWAY)?;
    if bus_ids.is_empty() {
        return Ok(Json(serde_json::json!({ "twins": [] })));
    }
    let keys: Vec<String> = bus_ids.iter().map(|b| format!("{}{}", TWIN_KEY_PREFIX, b)).collect();
    let values: Vec<Option<String>> = redis.get(keys).await.map_err(|_| StatusCode::BAD_GATEWAY)?;
    let twins: Vec<TwinState> = values
        .into_iter()
        .flatten()
        .filter_map(|j| serde_json::from_str(&j).ok())
        .collect();
    Ok(Json(serde_json::json!({ "twins": twins, "count": twins.len() })))
}
