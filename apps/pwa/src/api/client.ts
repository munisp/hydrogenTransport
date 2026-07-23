import { getAccessToken } from "../auth/keycloak";
import { config } from "../config";

/**
 * Thin fetch wrapper used by every domain client. Attaches the Keycloak bearer
 * token, normalises errors, and tolerates both envelope (`{ data: ... }`) and
 * bare JSON responses from the Go services behind APISIX.
 */

export class ApiError extends Error {
  readonly status: number;
  readonly path: string;
  readonly body: string;

  constructor(status: number, path: string, body: string) {
    super(`API ${status} for ${path}${body ? `: ${body.slice(0, 200)}` : ""}`);
    this.name = "ApiError";
    this.status = status;
    this.path = path;
    this.body = body;
  }
}

type QueryValue = string | number | boolean | null | undefined;

export function buildQuery(params: Record<string, QueryValue>): string {
  const search = new URLSearchParams();
  for (const [key, value] of Object.entries(params)) {
    if (value === null || value === undefined || value === "") continue;
    search.set(key, String(value));
  }
  const qs = search.toString();
  return qs ? `?${qs}` : "";
}

export async function apiFetch<T>(path: string, init?: RequestInit): Promise<T> {
  const headers = new Headers(init?.headers);
  headers.set("Accept", "application/json");
  const token = getAccessToken();
  if (token) headers.set("Authorization", `Bearer ${token}`);
  if (init?.body && !headers.has("Content-Type")) {
    headers.set("Content-Type", "application/json");
  }

  let res: Response;
  try {
    res = await fetch(`${config.apiBase}${path}`, { ...init, headers });
  } catch (err) {
    throw new ApiError(0, path, err instanceof Error ? err.message : "network error");
  }

  if (!res.ok) {
    const body = await res.text().catch(() => "");
    throw new ApiError(res.status, path, body);
  }
  if (res.status === 204) return undefined as T;
  return (await res.json()) as T;
}

/**
 * Unwrap `{ data: T }` envelopes while passing bare payloads through, so the UI
 * works against either response convention.
 */
export function unwrap<T>(payload: unknown): T {
  if (payload && typeof payload === "object" && "data" in (payload as Record<string, unknown>)) {
    return (payload as { data: T }).data;
  }
  return payload as T;
}

/** Same as unwrap but guarantees an array (list endpoints). */
export function unwrapList<T>(payload: unknown): T[] {
  const value = unwrap<unknown>(payload);
  if (Array.isArray(value)) return value as T[];
  // Tolerate keyed collections such as { vehicles: [...] } / { items: [...] }.
  if (value && typeof value === "object") {
    for (const key of [
      "items",
      "results",
      "vehicles",
      "stations",
      "incidents",
      "predictions",
      "fuel_levels",
      "twins",
      "jobs",
      "reports",
      "work_orders",
      "bays",
      "stops",
      "routes",
      "arrivals",
      "alerts",
      "requests",
      "credits",
      "datasets",
      "offers",
      "payments",
      "trades",
      "campaigns",
    ]) {
      const nested = (value as Record<string, unknown>)[key];
      if (Array.isArray(nested)) return nested as T[];
    }
  }
  return [];
}
