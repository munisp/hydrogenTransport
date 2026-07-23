import { API_PREFIX } from "../config";
import { apiFetch, buildQuery, unwrapList } from "./client";
import type {
  FuelReading,
  MaintenancePrediction,
  OptimizationPlan,
  OptimizationRequest,
  PredictResponse,
  TelemetrySample,
  TwinState,
  Vehicle,
} from "./types";

/** Fleet domain: fleet-api + digital-twin + ML + optimizer (SPEC §3.6). */

export async function listVehicles(): Promise<Vehicle[]> {
  const raw = await apiFetch<unknown>(`${API_PREFIX.fleet}/v1/vehicles`);
  return unwrapList<Vehicle>(raw);
}

export async function getVehicle(id: string): Promise<Vehicle> {
  return apiFetch<Vehicle>(`${API_PREFIX.fleet}/v1/vehicles/${encodeURIComponent(id)}`);
}

export async function latestTelemetry(): Promise<TelemetrySample[]> {
  const raw = await apiFetch<unknown>(`${API_PREFIX.fleet}/v1/telemetry/latest`);
  return unwrapList<TelemetrySample>(raw);
}

export async function listMaintenancePredictions(
  params: { bus_id?: string; min_risk?: number } = {},
): Promise<MaintenancePrediction[]> {
  const raw = await apiFetch<unknown>(
    `${API_PREFIX.fleet}/v1/maintenance/predictions${buildQuery({
      bus_id: params.bus_id,
      min_risk: params.min_risk,
    })}`,
  );
  return unwrapList<MaintenancePrediction>(raw);
}

/**
 * Trigger an on-demand failure-risk scoring run (predictive-maintenance ML
 * service). Returns the per-component risk scores directly.
 */
export async function triggerPrediction(busId: string): Promise<PredictResponse> {
  return apiFetch<PredictResponse>(`${API_PREFIX.ml}/v1/predict`, {
    method: "POST",
    body: JSON.stringify({ bus_id: busId }),
  });
}

export async function listFuelReadings(): Promise<FuelReading[]> {
  const raw = await apiFetch<unknown>(`${API_PREFIX.fleet}/v1/fuel/levels`);
  return unwrapList<FuelReading>(raw);
}

export async function getTwin(busId: string): Promise<TwinState> {
  return apiFetch<TwinState>(`${API_PREFIX.twin}/v1/twin/${encodeURIComponent(busId)}`);
}

export async function listTwins(): Promise<TwinState[]> {
  const raw = await apiFetch<unknown>(`${API_PREFIX.twin}/v1/twin`);
  return unwrapList<TwinState>(raw);
}

/** Request an OR-Tools route + refuel plan (route-optimizer service). */
export async function requestOptimization(req: OptimizationRequest): Promise<OptimizationPlan> {
  return apiFetch<OptimizationPlan>(`${API_PREFIX.optimize}/v1/optimize/route`, {
    method: "POST",
    body: JSON.stringify(req),
  });
}
