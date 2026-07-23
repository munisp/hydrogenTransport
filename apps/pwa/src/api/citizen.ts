import { API_PREFIX } from "../config";
import { apiFetch, buildQuery, unwrapList } from "./client";
import type {
  Arrival,
  CarbonCredit,
  DrtRequest,
  GeoPoint,
  JourneyOption,
  OpenDataset,
  ServiceAlert,
  Stop,
} from "./types";

/** Citizen & Engagement domain (citizen-api). */

export async function listStops(): Promise<Stop[]> {
  const raw = await apiFetch<unknown>(`${API_PREFIX.citizen}/v1/passenger/stops`);
  return unwrapList<Stop>(raw);
}

export async function listArrivals(stopId: string): Promise<Arrival[]> {
  const raw = await apiFetch<unknown>(
    `${API_PREFIX.citizen}/v1/passenger/arrivals${buildQuery({ stop_id: stopId })}`,
  );
  return unwrapList<Arrival>(raw);
}

export async function listServiceAlerts(): Promise<ServiceAlert[]> {
  const raw = await apiFetch<unknown>(`${API_PREFIX.citizen}/v1/passenger/alerts`);
  return unwrapList<ServiceAlert>(raw);
}

/** Rule-based journey planner: GET /v1/passenger/journey?from=&to= (stop IDs). */
export async function planJourney(params: {
  from: string;
  to: string;
}): Promise<JourneyOption[]> {
  const raw = await apiFetch<{ options?: JourneyOption[] }>(
    `${API_PREFIX.citizen}/v1/passenger/journey${buildQuery({
      from: params.from,
      to: params.to,
    })}`,
  );
  return raw?.options ?? [];
}

export async function listDrtRequests(): Promise<DrtRequest[]> {
  const raw = await apiFetch<unknown>(`${API_PREFIX.citizen}/v1/drt/requests`);
  return unwrapList<DrtRequest>(raw);
}

export async function createDrtRequest(body: {
  pickup: GeoPoint;
  dropoff: GeoPoint;
  pickup_label?: string;
  dropoff_label?: string;
  passengers?: number;
}): Promise<DrtRequest> {
  return apiFetch<DrtRequest>(`${API_PREFIX.citizen}/v1/drt/requests`, {
    method: "POST",
    body: JSON.stringify(body),
  });
}

export async function cancelDrtRequest(id: string): Promise<DrtRequest> {
  return apiFetch<DrtRequest>(
    `${API_PREFIX.citizen}/v1/drt/requests/${encodeURIComponent(id)}/cancel`,
    { method: "POST" },
  );
}

export async function listCarbonCredits(): Promise<CarbonCredit[]> {
  const raw = await apiFetch<unknown>(`${API_PREFIX.citizen}/v1/carbon/credits`);
  return unwrapList<CarbonCredit>(raw);
}

export async function listOpenDatasets(): Promise<OpenDataset[]> {
  const raw = await apiFetch<unknown>(`${API_PREFIX.citizen}/v1/opendata/datasets`);
  return unwrapList<OpenDataset>(raw);
}
