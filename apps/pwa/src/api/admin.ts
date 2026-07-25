import { apiFetch, buildQuery } from "./client";
import { API_PREFIX } from "../config";

/**
 * admin-api client (APISIX prefix /api/admin, service serves /v1/*).
 *
 * Surface split (services/go/admin-api/internal/server/server.go):
 *  - POST /v1/onboarding/citizen + /v1/onboarding/{persona}: public (no JWT)
 *  - onboarding list/approve/reject + /v1/admin/*: operator OR platform-admin
 *  - /v1/users/* + PUT /v1/admin/toggles/{module}: platform-admin only
 */

// ---- Types -------------------------------------------------------------------

export type OnboardingPersona =
  | "citizen"
  | "driver"
  | "operator"
  | "station-staff"
  | "advertiser"
  | "data-partner"
  | "gov-viewer";

export type OnboardingStatus = "pending" | "approved" | "rejected" | "completed";

/** Mirrors admin-api internal/onboarding.Request. */
export interface OnboardingRequest {
  id: string;
  persona: OnboardingPersona;
  email: string;
  display_name: string;
  org: string;
  status: OnboardingStatus;
  keycloak_sub: string;
  meta?: Record<string, unknown> | null;
  created_at: string;
  decided_at?: string | null;
  decided_by?: string;
}

/** Mirrors admin-api internal/keycloak.User. */
export interface AdminUser {
  id: string;
  username: string;
  email: string;
  first_name: string;
  last_name: string;
  enabled: boolean;
  roles: string[];
}

/**
 * Aggregated KPIs from GET /v1/admin/kpis. Domain payloads are `null` when the
 * backing source timed out (meta.degraded lists the failed sources).
 */
export interface AdminKpis {
  generated_at: string;
  fleet: {
    vehicles_total: number;
    vehicles_available: number;
    telemetry_points_per_min: number;
  } | null;
  infra: { open_incidents: number } | null;
  citizen: {
    drt_requests_today: number;
    carbon_kg_co2_total: number;
  } | null;
  commerce: {
    payments_30d: number;
    revenue_30d_minor: number;
    currency: string;
  } | null;
  toggles: {
    modules_enabled: number;
    modules_total: number;
    domains: Record<string, { enabled: number; total: number }>;
  } | null;
  meta: { partial: boolean; degraded: string[] };
}

/** One health probe result from GET /v1/admin/health. */
export interface HealthCheck {
  name: string;
  kind: string;
  target: string;
  status: "up" | "down" | "degraded" | string;
  latency_ms: number;
}

export interface HealthResponse {
  generated_at: string;
  checks: HealthCheck[];
  summary: { up: number; down: number };
}

/** Alertmanager v2 alert object as proxied by GET /v1/admin/alerts. */
export interface OpsAlert {
  labels?: Record<string, string>;
  annotations?: Record<string, string>;
  startsAt?: string;
  endsAt?: string;
  status?: { state?: string };
}

/** Enriched toggle row from GET /v1/admin/toggles. */
export interface AdminToggle {
  module: string;
  domain: string;
  enabled: boolean;
  owning_services: string[];
}

const base = API_PREFIX.admin;

// ---- Onboarding (public intake) ----------------------------------------------

export async function onboardCitizen(body: {
  email: string;
  display_name: string;
  password: string;
}): Promise<{ request: OnboardingRequest; message: string }> {
  // The service provisions the account itself (temporary password + actions
  // email); the password field is accepted by the public contract.
  return apiFetch(`${base}/v1/onboarding/citizen`, {
    method: "POST",
    body: JSON.stringify(body),
  });
}

export async function submitOnboarding(
  persona: Exclude<OnboardingPersona, "citizen">,
  body: { email: string; display_name: string; org: string; meta?: Record<string, unknown> },
): Promise<{ request: OnboardingRequest; message?: string }> {
  return apiFetch(`${base}/v1/onboarding/${encodeURIComponent(persona)}`, {
    method: "POST",
    body: JSON.stringify(body),
  });
}

// ---- Onboarding queue (operator+) --------------------------------------------

interface ListEnvelope<T> {
  requests?: T[];
  request?: T;
}

export async function listOnboarding(params?: {
  status?: OnboardingStatus | "";
  persona?: OnboardingPersona | "";
}): Promise<OnboardingRequest[]> {
  const res = await apiFetch<ListEnvelope<OnboardingRequest> | OnboardingRequest[]>(
    `${base}/v1/onboarding${buildQuery({ status: params?.status, persona: params?.persona })}`,
  );
  if (Array.isArray(res)) return res;
  return res.requests ?? [];
}

export function approveOnboarding(id: string): Promise<unknown> {
  return apiFetch(`${base}/v1/onboarding/${encodeURIComponent(id)}/approve`, { method: "POST" });
}

export function rejectOnboarding(id: string, reason?: string): Promise<unknown> {
  return apiFetch(`${base}/v1/onboarding/${encodeURIComponent(id)}/reject`, {
    method: "POST",
    body: JSON.stringify(reason ? { reason } : {}),
  });
}

// ---- User management (platform-admin) -----------------------------------------

export async function listUsers(params?: { role?: string; q?: string }): Promise<AdminUser[]> {
  const res = await apiFetch<{ users?: AdminUser[] } | AdminUser[]>(
    `${base}/v1/users${buildQuery({ role: params?.role, q: params?.q })}`,
  );
  if (Array.isArray(res)) return res;
  return res.users ?? [];
}

export function createUser(body: {
  email: string;
  display_name: string;
  roles?: string[];
}): Promise<{ id: string }> {
  return apiFetch(`${base}/v1/users`, { method: "POST", body: JSON.stringify(body) });
}

export function updateUserRoles(
  id: string,
  body: { add?: string[]; remove?: string[] },
): Promise<unknown> {
  return apiFetch(`${base}/v1/users/${encodeURIComponent(id)}/roles`, {
    method: "PUT",
    body: JSON.stringify({ add: body.add ?? [], remove: body.remove ?? [] }),
  });
}

export function setUserEnabled(id: string, enabled: boolean): Promise<unknown> {
  return apiFetch(
    `${base}/v1/users/${encodeURIComponent(id)}/${enabled ? "enable" : "disable"}`,
    { method: "POST" },
  );
}

export function resetUserPassword(id: string): Promise<unknown> {
  return apiFetch(`${base}/v1/users/${encodeURIComponent(id)}/reset-password`, {
    method: "POST",
  });
}

// ---- Admin ops feed (operator+) ------------------------------------------------

export function getAdminKpis(): Promise<AdminKpis> {
  return apiFetch(`${base}/v1/admin/kpis`);
}

export async function getAdminHealth(): Promise<HealthResponse> {
  const res = await apiFetch<HealthResponse | HealthCheck[]>(`${base}/v1/admin/health`);
  // Tolerate a bare array shape as well as the {checks, summary} envelope.
  if (Array.isArray(res)) {
    const up = res.filter((c) => c.status === "up").length;
    return {
      generated_at: new Date().toISOString(),
      checks: res,
      summary: { up, down: res.length - up },
    };
  }
  return res;
}

export async function getAdminAlerts(): Promise<OpsAlert[]> {
  const res = await apiFetch<unknown>(`${base}/v1/admin/alerts`);
  return Array.isArray(res) ? (res as OpsAlert[]) : [];
}

export async function listAdminToggles(): Promise<AdminToggle[]> {
  const res = await apiFetch<unknown>(`${base}/v1/admin/toggles`);
  if (Array.isArray(res)) return res as AdminToggle[];
  if (res && typeof res === "object") {
    const obj = res as Record<string, unknown>;
    for (const key of ["toggles", "items", "data"]) {
      if (Array.isArray(obj[key])) return obj[key] as AdminToggle[];
    }
  }
  return [];
}

export function setAdminToggle(module: string, enabled: boolean): Promise<unknown> {
  return apiFetch(`${base}/v1/admin/toggles/${encodeURIComponent(module)}`, {
    method: "PUT",
    body: JSON.stringify({ enabled }),
  });
}
