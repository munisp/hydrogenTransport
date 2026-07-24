//! Feature-toggle gate. Polls toggle-service on an interval; fail-closed
//! (any error => module treated as disabled, SPEC.md §3.2).

use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::Arc;
use std::time::Duration;

use serde::Deserialize;

#[derive(Debug, Deserialize)]
struct ToggleResponse {
    enabled: bool,
}

/// Shared gate state; cheap to clone.
#[derive(Clone, Default)]
pub struct ToggleGate {
    enabled: Arc<AtomicBool>,
}

impl ToggleGate {
    pub fn new() -> Self {
        Self::default()
    }

    pub fn is_enabled(&self) -> bool {
        self.enabled.load(Ordering::Relaxed)
    }

    pub fn set(&self, enabled: bool) {
        self.enabled.store(enabled, Ordering::Relaxed);
    }
}

/// Poll `GET {toggle_url}/v1/toggles/{module}` every `interval` and update the
/// gate. Runs forever; cancel via `tokio::select!` in the caller.
pub async fn run_poller(gate: ToggleGate, toggle_url: String, module: String, interval: Duration) {
    let client = reqwest::Client::builder()
        .timeout(Duration::from_secs(2))
        .build()
        .expect("reqwest client");
    let url = format!("{}/v1/toggles/{}", toggle_url.trim_end_matches('/'), module);
    loop {
        match client.get(&url).send().await {
            Ok(resp) if resp.status().is_success() => match resp.json::<ToggleResponse>().await {
                Ok(body) => {
                    let prev = gate.is_enabled();
                    gate.set(body.enabled);
                    if prev != body.enabled {
                        tracing::info!(module = %module, enabled = body.enabled, "toggle changed");
                    }
                }
                Err(err) => {
                    tracing::warn!(error = %err, "toggle response malformed; failing closed");
                    gate.set(false);
                }
            },
            Ok(resp) => {
                tracing::warn!(status = %resp.status(), "toggle-service error; failing closed");
                gate.set(false);
            }
            Err(err) => {
                tracing::warn!(error = %err, "toggle-service unreachable; failing closed");
                gate.set(false);
            }
        }
        tokio::time::sleep(interval).await;
    }
}
