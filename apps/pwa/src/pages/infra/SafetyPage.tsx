import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Check, Hand } from "lucide-react";
import { acknowledgeIncident, listIncidents, resolveIncident } from "../../api/infra";
import type { Incident } from "../../api/types";
import {
  Badge,
  Button,
  Card,
  CardContent,
  CardHeader,
  CardTitle,
  ErrorState,
  PageHeader,
  Spinner,
  StatusBadge,
  Table,
  Td,
  Th,
} from "../../components/ui";
import { formatDateTime } from "../../lib/format";

const severityTone: Record<string, "stone" | "amber" | "red"> = {
  low: "stone",
  medium: "amber",
  high: "red",
  critical: "red",
};

/** leak-detection — incident workflow (ack/resolve) plus the live leak alert feed. */
export default function SafetyPage() {
  const queryClient = useQueryClient();
  const incidents = useQuery({
    queryKey: ["infra", "incidents", "open"],
    queryFn: () => listIncidents({ status: "open" }),
    refetchInterval: 15_000,
  });
  const leaks = useQuery({
    queryKey: ["infra", "incidents", "leaks"],
    queryFn: () => listIncidents({ type: "leak" }),
    refetchInterval: 15_000,
  });

  const ack = useMutation({
    mutationFn: acknowledgeIncident,
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ["infra", "incidents"] }),
  });
  const resolve = useMutation({
    mutationFn: resolveIncident,
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ["infra", "incidents"] }),
  });

  const open = (incidents.data ?? []).filter((i) => i.status !== "resolved");
  const leakFeed = [...(leaks.data ?? [])].sort((a, b) => b.opened_at.localeCompare(a.opened_at));

  return (
    <div>
      <PageHeader
        title="Safety & Leak Detection"
        description="H2 leak sensor alarms and the incident workflow. Acknowledge to claim, resolve when mitigated."
      />

      <div className="grid grid-cols-1 gap-4 xl:grid-cols-3">
        <Card className="xl:col-span-2">
          <CardHeader>
            <CardTitle>Open incidents</CardTitle>
          </CardHeader>
          <CardContent className="p-0">
            {incidents.isLoading ? (
              <Spinner />
            ) : incidents.isError ? (
              <div className="p-5">
                <ErrorState error={incidents.error} onRetry={() => incidents.refetch()} />
              </div>
            ) : (
              <Table>
                <thead>
                  <tr>
                    <Th>Type</Th>
                    <Th>Severity</Th>
                    <Th>Asset</Th>
                    <Th>Status</Th>
                    <Th>Opened</Th>
                    <Th className="text-right">Actions</Th>
                  </tr>
                </thead>
                <tbody>
                  {open.length === 0 ? (
                    <tr>
                      <Td colSpan={6} className="py-10 text-center text-stone-400">
                        No open incidents — all clear.
                      </Td>
                    </tr>
                  ) : (
                    open.map((incident) => (
                      <IncidentRow
                        key={incident.id}
                        incident={incident}
                        onAck={() => ack.mutate(incident.id)}
                        onResolve={() => resolve.mutate(incident.id)}
                        busy={
                          (ack.isPending && ack.variables === incident.id) ||
                          (resolve.isPending && resolve.variables === incident.id)
                        }
                      />
                    ))
                  )}
                </tbody>
              </Table>
            )}
          </CardContent>
        </Card>

        <Card>
          <CardHeader>
            <CardTitle>Leak alert feed</CardTitle>
          </CardHeader>
          <CardContent className="space-y-3">
            {leaks.isLoading ? (
              <Spinner className="py-6" />
            ) : leaks.isError ? (
              <ErrorState error={leaks.error} onRetry={() => leaks.refetch()} />
            ) : leakFeed.length === 0 ? (
              <p className="py-6 text-center text-sm text-stone-400">
                No leak events detected.
              </p>
            ) : (
              leakFeed.slice(0, 12).map((leak) => (
                <div
                  key={leak.id}
                  className="rounded-lg border border-stone-200 bg-surface-sunken px-3 py-2.5"
                >
                  <div className="flex items-center justify-between gap-2">
                    <Badge tone={severityTone[leak.severity] ?? "stone"}>{leak.severity}</Badge>
                    <StatusBadge status={leak.status} />
                  </div>
                  <p className="mt-1.5 text-xs text-stone-600">
                    {describeLeak(leak)}
                  </p>
                  <p className="mt-1 text-[11px] text-stone-400">{formatDateTime(leak.opened_at)}</p>
                </div>
              ))
            )}
          </CardContent>
        </Card>
      </div>
    </div>
  );
}

function describeLeak(leak: Incident): string {
  const sensor = typeof leak.meta?.sensor_id === "string" ? leak.meta.sensor_id : null;
  const ppm =
    typeof leak.meta?.concentration_ppm === "number" ? `${leak.meta.concentration_ppm} ppm` : null;
  const where = leak.station_id
    ? `station ${leak.station_id.slice(0, 8)}`
    : leak.bus_id
      ? `bus ${leak.bus_id.slice(0, 8)}`
      : "unknown asset";
  return [`Leak at ${where}`, sensor && `sensor ${sensor}`, ppm].filter(Boolean).join(" · ");
}

function IncidentRow({
  incident,
  onAck,
  onResolve,
  busy,
}: {
  incident: Incident;
  onAck: () => void;
  onResolve: () => void;
  busy: boolean;
}) {
  return (
    <tr className="hover:bg-surface-sunken/60">
      <Td className="font-medium text-stone-800">{incident.type.replace(/_/g, " ")}</Td>
      <Td>
        <Badge tone={severityTone[incident.severity] ?? "stone"}>{incident.severity}</Badge>
      </Td>
      <Td className="font-mono text-xs text-stone-500">
        {incident.bus_id
          ? `bus:${incident.bus_id.slice(0, 8)}`
          : incident.station_id
            ? `stn:${incident.station_id.slice(0, 8)}`
            : "—"}
      </Td>
      <Td>
        <StatusBadge status={incident.status} />
      </Td>
      <Td className="text-stone-500">{formatDateTime(incident.opened_at)}</Td>
      <Td>
        <div className="flex justify-end gap-2">
          {incident.status === "open" ? (
            <Button variant="secondary" busy={busy} onClick={onAck}>
              <Hand className="h-3.5 w-3.5" aria-hidden />
              Ack
            </Button>
          ) : null}
          {incident.status !== "resolved" ? (
            <Button variant="ghost" busy={busy} onClick={onResolve}>
              <Check className="h-3.5 w-3.5" aria-hidden />
              Resolve
            </Button>
          ) : null}
        </div>
      </Td>
    </tr>
  );
}
