import { API_PREFIX } from "../config";
import { apiFetch, buildQuery, unwrapList } from "./client";
import type {
  ComplianceReport,
  DepotBay,
  DispatchJob,
  Incident,
  Station,
  WorkOrder,
} from "./types";

/** Infrastructure & Safety domain (infra-api). */

export async function listStations(): Promise<Station[]> {
  const raw = await apiFetch<unknown>(`${API_PREFIX.infra}/v1/stations`);
  return unwrapList<Station>(raw);
}

export async function listIncidents(
  params: { status?: string; type?: string } = {},
): Promise<Incident[]> {
  const raw = await apiFetch<unknown>(
    `${API_PREFIX.infra}/v1/incidents${buildQuery({ status: params.status, type: params.type })}`,
  );
  return unwrapList<Incident>(raw);
}

export async function acknowledgeIncident(id: string): Promise<Incident> {
  return apiFetch<Incident>(`${API_PREFIX.infra}/v1/incidents/${encodeURIComponent(id)}/ack`, {
    method: "POST",
  });
}

export async function resolveIncident(id: string): Promise<Incident> {
  return apiFetch<Incident>(
    `${API_PREFIX.infra}/v1/incidents/${encodeURIComponent(id)}/resolve`,
    { method: "POST" },
  );
}

export async function reportIncident(body: {
  type: string;
  severity: string;
  bus_id?: string | null;
  station_id?: string | null;
  description: string;
}): Promise<Incident> {
  return apiFetch<Incident>(`${API_PREFIX.infra}/v1/incidents`, {
    method: "POST",
    body: JSON.stringify(body),
  });
}

export async function listDispatchJobs(
  params: { driver_sub?: string; status?: string } = {},
): Promise<DispatchJob[]> {
  const raw = await apiFetch<unknown>(
    `${API_PREFIX.infra}/v1/dispatch/jobs${buildQuery({
      driver_sub: params.driver_sub,
      status: params.status,
    })}`,
  );
  return unwrapList<DispatchJob>(raw);
}

export async function acceptDispatchJob(id: string): Promise<DispatchJob> {
  return apiFetch<DispatchJob>(
    `${API_PREFIX.infra}/v1/dispatch/jobs/${encodeURIComponent(id)}/accept`,
    { method: "POST" },
  );
}

export async function listComplianceReports(): Promise<ComplianceReport[]> {
  const raw = await apiFetch<unknown>(`${API_PREFIX.infra}/v1/compliance/reports`);
  return unwrapList<ComplianceReport>(raw);
}

/** POST /v1/compliance/reports/generate — the backend aggregates the report
 * server-side and ignores the request body. */
export async function generateComplianceReport(): Promise<ComplianceReport> {
  return apiFetch<ComplianceReport>(`${API_PREFIX.infra}/v1/compliance/reports/generate`, {
    method: "POST",
  });
}

export async function listDepotBays(): Promise<DepotBay[]> {
  const raw = await apiFetch<unknown>(`${API_PREFIX.infra}/v1/depot/bays`);
  return unwrapList<DepotBay>(raw);
}

export async function listWorkOrders(): Promise<WorkOrder[]> {
  const raw = await apiFetch<unknown>(`${API_PREFIX.infra}/v1/depot/work-orders`);
  return unwrapList<WorkOrder>(raw);
}
