import { config } from "../config";

/**
 * Compact API client mirroring the PWA contracts. Field names follow the
 * Postgres schemas in SPEC §3.4; paths follow the APISIX prefixes in §3.6.
 * Fail-closed toggle check identical in spirit to @h2fleet/toggle-client.
 */

// ---- Types (subset of the PWA shapes used by the app) -----------------------

/** citizen-api GTFS stops.txt style. */
export interface Stop {
  stop_id: string;
  stop_name: string;
  stop_lat: number;
  stop_lon: number;
}

/** citizen-api — one upcoming departure at a stop. */
export interface Arrival {
  route_id: string;
  route_short_name: string;
  headsign: string;
  stop_id: string;
  scheduled_at: string;
  in_minutes: number;
}

/** citizen-api — GTFS-RT style service alert. */
export interface ServiceAlert {
  id: string;
  header: string;
  description: string;
  severity: "info" | "warning" | "severe" | string;
  route_ids?: string[]; // omitempty
}

/** citizen-api — pickup/dropoff emitted as scalar lat/lon (omitempty). */
export interface DrtRequest {
  id: string;
  user_sub: string;
  pickup_lat?: number | null;
  pickup_lon?: number | null;
  dropoff_lat?: number | null;
  dropoff_lon?: number | null;
  status: string;
  requested_at: string;
}

export interface CarbonCredit {
  id: string;
  period: string;
  kg_co2_avoided: number;
  credits: number;
  issued_at: string;
}

/** infra-api — mirrors infra.dispatch_jobs. */
export interface DispatchJob {
  id: string;
  driver_sub: string;
  vehicle_id?: string | null; // omitempty
  route: string;
  starts_at?: string | null; // omitempty
  status: string;
  created_at: string;
  accepted_at?: string | null; // omitempty
}

// ---- HTTP core ----------------------------------------------------------------

export class ApiError extends Error {
  constructor(
    readonly status: number,
    readonly path: string,
    body: string,
  ) {
    super(`API ${status} for ${path}${body ? `: ${body.slice(0, 160)}` : ""}`);
    this.name = "ApiError";
  }
}

let accessToken: string | undefined;

/** Set after OIDC login (wired in a later iteration of the auth flow). */
export function setAccessToken(token: string | undefined): void {
  accessToken = token;
}

async function apiFetch<T>(path: string, init?: RequestInit): Promise<T> {
  const headers: Record<string, string> = {
    Accept: "application/json",
    ...(init?.body ? { "Content-Type": "application/json" } : {}),
    ...(accessToken ? { Authorization: `Bearer ${accessToken}` } : {}),
    ...((init?.headers as Record<string, string> | undefined) ?? {}),
  };
  let res: Response;
  try {
    res = await fetch(`${config.apiBase}${path}`, { ...init, headers });
  } catch (err) {
    throw new ApiError(0, path, err instanceof Error ? err.message : "network error");
  }
  if (!res.ok) {
    throw new ApiError(res.status, path, await res.text().catch(() => ""));
  }
  if (res.status === 204) return undefined as T;
  return (await res.json()) as T;
}

function unwrapList<T>(payload: unknown): T[] {
  if (Array.isArray(payload)) return payload as T[];
  if (payload && typeof payload === "object") {
    const obj = payload as Record<string, unknown>;
    if (Array.isArray(obj.data)) return obj.data as T[];
    for (const key of [
      "items",
      "results",
      "jobs",
      "requests",
      "credits",
      "stops",
      "arrivals",
      "alerts",
    ]) {
      if (Array.isArray(obj[key])) return obj[key] as T[];
    }
  }
  return [];
}

// ---- Citizen endpoints (citizen-api) ------------------------------------------

export const api = {
  listStops: () =>
    apiFetch<unknown>("/api/citizen/v1/passenger/stops").then((r) => unwrapList<Stop>(r)),
  listArrivals: (stopId: string) =>
    apiFetch<unknown>(
      `/api/citizen/v1/passenger/arrivals?stop_id=${encodeURIComponent(stopId)}`,
    ).then((r) => unwrapList<Arrival>(r)),
  listAlerts: () =>
    apiFetch<unknown>("/api/citizen/v1/passenger/alerts").then((r) =>
      unwrapList<ServiceAlert>(r),
    ),
  planJourney: (from: string, to: string) =>
    apiFetch<{ options?: unknown[] }>(
      `/api/citizen/v1/passenger/journey?from=${encodeURIComponent(from)}&to=${encodeURIComponent(to)}`,
    ).then((r) => r?.options ?? []),
  createDrtRequest: (body: {
    pickup: { lat: number; lon: number };
    dropoff: { lat: number; lon: number };
  }) =>
    apiFetch<DrtRequest>("/api/citizen/v1/drt/requests", {
      method: "POST",
      body: JSON.stringify(body),
    }),
  listDrtRequests: () =>
    apiFetch<unknown>("/api/citizen/v1/drt/requests").then((r) => unwrapList<DrtRequest>(r)),
  listCarbonCredits: () =>
    apiFetch<unknown>("/api/citizen/v1/carbon/credits").then((r) => unwrapList<CarbonCredit>(r)),

  // ---- Driver endpoints (infra-api) ----
  listDispatchJobs: (driverSub?: string) =>
    apiFetch<unknown>(
      `/api/infra/v1/dispatch/jobs${driverSub ? `?driver_sub=${encodeURIComponent(driverSub)}` : ""}`,
    ).then((r) => unwrapList<DispatchJob>(r)),
  acceptDispatchJob: (id: string) =>
    apiFetch<DispatchJob>(`/api/infra/v1/dispatch/jobs/${encodeURIComponent(id)}/accept`, {
      method: "POST",
    }),
  reportIncident: (body: {
    type: string;
    severity: string;
    bus_id?: string | null;
    description: string;
  }) =>
    apiFetch<{ id: string }>("/api/infra/v1/incidents", {
      method: "POST",
      body: JSON.stringify(body),
    }),

  // ---- Toggle service (fail-closed) ----
  getToggles: async (): Promise<Record<string, boolean>> => {
    try {
      const res = await apiFetch<{ toggles?: Record<string, boolean> }>(
        "/api/toggles/v1/toggles",
      );
      return res?.toggles ?? {};
    } catch {
      return {}; // fail closed: unknown => disabled
    }
  },
};
