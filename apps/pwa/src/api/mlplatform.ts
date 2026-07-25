import { apiFetch } from "./client";
import { API_PREFIX } from "../config";

/**
 * ml-platform client (APISIX prefix /api/mlplatform, service serves /v1/ml/*).
 *
 * Shapes mirror services/python/ml-platform/app:
 *  - GET /v1/ml/models  -> dict keyed by model name with champion/challenger
 *    variants + registry pointers (NOT a flat array).
 *  - GET /v1/ml/drift   -> dict keyed by model name with a status + per-feature
 *    PSI/KS report.
 *  - POST /v1/ml/maintenance/score requires {bus_id, window} where each window
 *    row carries SEQ_FEATURES (h2_level_pct, fuel_cell_kw, battery_soc_pct,
 *    speed_kph, ambient_temp_c); 8..2048 timesteps.
 */

const base = API_PREFIX.mlplatform;

export const MODEL_NAMES = [
  "maintenance_lstm",
  "demand_forecaster",
  "leak_autoencoder",
  "fleet_gcn",
  "carbon_forecaster",
] as const;

export type ModelName = (typeof MODEL_NAMES)[number];

export const MODEL_LABELS: Record<ModelName, string> = {
  maintenance_lstm: "Maintenance LSTM",
  demand_forecaster: "Demand Forecaster",
  leak_autoencoder: "Leak Autoencoder",
  fleet_gcn: "Fleet GCN",
  carbon_forecaster: "Carbon Forecaster",
};

/** Feature order for the maintenance LSTM window rows (models.SEQ_FEATURES). */
export const SEQ_FEATURES = [
  "h2_level_pct",
  "fuel_cell_kw",
  "battery_soc_pct",
  "speed_kph",
  "ambient_temp_c",
] as const;

// ---- Model registry -----------------------------------------------------------

export interface ModelVariant {
  version: string;
  metrics: Record<string, number | string>;
  n_params?: number | null;
  trained_at?: string | null;
}

export interface ModelEntry {
  name: ModelName;
  champion?: ModelVariant;
  challenger?: ModelVariant;
  registry: { champion?: string | null; challenger?: string | null };
  loaded: boolean;
}

type RawModelsResponse = Record<
  string,
  {
    champion?: ModelVariant;
    challenger?: ModelVariant;
    registry?: { champion?: string | null; challenger?: string | null };
    loaded?: boolean;
  }
>;

export async function listModels(): Promise<ModelEntry[]> {
  const res = await apiFetch<RawModelsResponse>(`${base}/v1/ml/models`);
  return MODEL_NAMES.map((name) => {
    const raw = res?.[name] ?? {};
    return {
      name,
      champion: raw.champion,
      challenger: raw.challenger,
      registry: raw.registry ?? {},
      loaded: raw.loaded === true,
    };
  });
}

// ---- Drift --------------------------------------------------------------------

export interface DriftFeatureReport {
  psi: number;
  ks: number;
  drifted?: boolean;
}

export interface DriftEntry {
  model: ModelName;
  /** no-data | insufficient-data | ok | drift (server-computed). */
  status: string;
  n_observed?: number;
  worst_psi?: number | null;
  features: Record<string, DriftFeatureReport>;
}

type RawDriftResponse = Record<
  string,
  {
    model?: string;
    status?: string;
    n_observed?: number;
    worst_psi?: number;
    features?: Record<string, DriftFeatureReport>;
  }
>;

export async function listDrift(): Promise<DriftEntry[]> {
  const res = await apiFetch<RawDriftResponse>(`${base}/v1/ml/drift`);
  return MODEL_NAMES.map((name) => {
    const raw = res?.[name] ?? {};
    const features = raw.features ?? {};
    const worst =
      typeof raw.worst_psi === "number"
        ? raw.worst_psi
        : Object.values(features).reduce<number>(
            (acc, f) => Math.max(acc, typeof f?.psi === "number" ? f.psi : 0),
            0,
          );
    return {
      model: name,
      status: raw.status ?? "no-data",
      n_observed: raw.n_observed,
      worst_psi: Object.keys(features).length > 0 ? worst : null,
      features,
    };
  });
}

// ---- Scoring -------------------------------------------------------------------

export interface MaintenancePrediction {
  component: string;
  risk_score: number;
  days_to_failure: number;
}

export interface MaintenanceScoreResult {
  predictions: MaintenancePrediction[];
  variant: string;
  model_version: string;
}

/**
 * Build a plausible nominal telemetry window for the demo "score a bus" panel.
 * Real deployments would pull the live window from fleet-api telemetry; here
 * we synthesise a gently varying baseline so the LSTM has valid input
 * (8..2048 rows x 5 features per the pydantic contract).
 */
export function synthesizeMaintenanceWindow(timesteps = 48): number[][] {
  const rows: number[][] = [];
  for (let t = 0; t < timesteps; t++) {
    const phase = t / 6;
    rows.push([
      62 + Math.sin(phase) * 4, // h2_level_pct
      75 + Math.sin(phase / 2) * 12, // fuel_cell_kw
      68 + Math.cos(phase / 3) * 6, // battery_soc_pct
      32 + Math.sin(phase / 1.5) * 18, // speed_kph
      14 + Math.cos(phase / 4) * 3, // ambient_temp_c
    ]);
  }
  return rows.map((row) => row.map((v) => Math.round(v * 100) / 100));
}

export function scoreMaintenance(
  busId: string,
  window: number[][],
): Promise<MaintenanceScoreResult> {
  return apiFetch(`${base}/v1/ml/maintenance/score`, {
    method: "POST",
    body: JSON.stringify({ bus_id: busId, window }),
  });
}
