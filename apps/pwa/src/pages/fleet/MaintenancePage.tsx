import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Play } from "lucide-react";
import { listMaintenancePredictions, listVehicles, triggerPrediction } from "../../api/fleet";
import {
  Badge,
  Button,
  Card,
  ErrorState,
  Field,
  PageHeader,
  ProgressBar,
  Select,
  Spinner,
  Table,
  Td,
  Th,
} from "../../components/ui";
import { formatDateTime, formatNumber, riskTone } from "../../lib/format";

const toneBadge = { low: "green", medium: "amber", high: "red" } as const;

/** predictive-maintenance — ML failure-risk scoring on fuel-cell/battery/H2 systems. */
export default function MaintenancePage() {
  const queryClient = useQueryClient();
  const predictions = useQuery({
    queryKey: ["fleet", "maintenance", "predictions"],
    queryFn: () => listMaintenancePredictions(),
  });
  const vehicles = useQuery({ queryKey: ["fleet", "vehicles"], queryFn: listVehicles });
  const [busId, setBusId] = useState("");
  const effectiveBusId = busId || vehicles.data?.[0]?.id || "";

  const trigger = useMutation({
    mutationFn: (id: string) => triggerPrediction(id),
    onSuccess: () =>
      queryClient.invalidateQueries({ queryKey: ["fleet", "maintenance", "predictions"] }),
  });

  const fleetNo = new Map((vehicles.data ?? []).map((v) => [v.id, v.fleet_no]));
  const sorted = [...(predictions.data ?? [])].sort((a, b) => b.risk_score - a.risk_score);
  const highRisk = sorted.filter((p) => p.risk_score >= 0.75).length;

  return (
    <div>
      <PageHeader
        title="Predictive Maintenance"
        description="Failure-risk scores from the ML service (rule-based fallback when no trained model is deployed)."
        actions={
          <div className="flex items-end gap-3">
            <Field label="Bus">
              <Select value={effectiveBusId} onChange={(e) => setBusId(e.target.value)}>
                {(vehicles.data ?? []).map((v) => (
                  <option key={v.id} value={v.id}>
                    {v.fleet_no}
                  </option>
                ))}
              </Select>
            </Field>
            <Button
              onClick={() => effectiveBusId && trigger.mutate(effectiveBusId)}
              busy={trigger.isPending}
              disabled={!effectiveBusId}
            >
              <Play className="h-4 w-4" aria-hidden />
              Run prediction now
            </Button>
          </div>
        }
      />

      {trigger.data ? (
        <Card className="mb-4">
          <div className="px-4 py-3">
            <p className="text-sm font-medium text-stone-800">
              Fresh scoring for {fleetNo.get(trigger.data.bus_id) ?? trigger.data.bus_id.slice(0, 8)}{" "}
              <span className="ml-2 font-mono text-xs text-stone-500">
                model {trigger.data.model_version} · {trigger.data.feature_window_hours}h feature
                window
              </span>
            </p>
            <ul className="mt-3 space-y-2">
              {trigger.data.predictions.map((p) => {
                const tone = riskTone(p.risk_score);
                return (
                  <li key={p.component} className="flex items-center gap-3 text-sm">
                    <span className="w-40 shrink-0 text-stone-700">
                      {p.component.replace(/_/g, " ")}
                    </span>
                    <ProgressBar
                      valuePct={p.risk_score * 100}
                      tone={tone === "high" ? "red" : tone === "medium" ? "amber" : "teal"}
                      className="w-28"
                    />
                    <Badge tone={toneBadge[tone]}>{formatNumber(p.risk_score * 100, 0)}%</Badge>
                    <span className="text-xs text-stone-500">
                      failure predicted {formatDateTime(p.predicted_failure_at)}
                    </span>
                  </li>
                );
              })}
            </ul>
          </div>
        </Card>
      ) : null}

      {highRisk > 0 ? (
        <div className="mb-4 rounded-xl border border-red-200 bg-red-50 px-4 py-3 text-sm text-red-800">
          {highRisk} component{highRisk === 1 ? "" : "s"} above the 75% risk threshold — schedule
          workshop slots before the predicted failure windows.
        </div>
      ) : null}

      {predictions.isLoading ? (
        <Spinner />
      ) : predictions.isError ? (
        <ErrorState error={predictions.error} onRetry={() => predictions.refetch()} />
      ) : (
        <Card>
          <Table>
            <thead>
              <tr>
                <Th>Bus</Th>
                <Th>Component</Th>
                <Th className="w-56">Risk score</Th>
                <Th>Predicted failure</Th>
                <Th>Model</Th>
                <Th>Scored</Th>
              </tr>
            </thead>
            <tbody>
              {sorted.length === 0 ? (
                <tr>
                  <Td colSpan={6} className="py-10 text-center text-stone-400">
                    No predictions yet — run a scoring pass to populate this table.
                  </Td>
                </tr>
              ) : (
                sorted.map((p) => {
                  const tone = riskTone(p.risk_score);
                  return (
                    <tr key={p.id} className="hover:bg-surface-sunken/60">
                      <Td className="font-medium text-stone-800">
                        {fleetNo.get(p.bus_id) ?? p.bus_id.slice(0, 8)}
                      </Td>
                      <Td>{p.component.replace(/_/g, " ")}</Td>
                      <Td>
                        <div className="flex items-center gap-2">
                          <ProgressBar
                            valuePct={p.risk_score * 100}
                            tone={tone === "high" ? "red" : tone === "medium" ? "amber" : "teal"}
                            className="w-28"
                          />
                          <Badge tone={toneBadge[tone]}>{formatNumber(p.risk_score * 100, 0)}%</Badge>
                        </div>
                      </Td>
                      <Td className="text-stone-600">{formatDateTime(p.predicted_failure_at)}</Td>
                      <Td className="font-mono text-xs text-stone-500">{p.model_version}</Td>
                      <Td className="text-stone-500">{formatDateTime(p.created_at)}</Td>
                    </tr>
                  );
                })
              )}
            </tbody>
          </Table>
        </Card>
      )}
      {trigger.isError ? (
        <p className="mt-4 text-sm text-red-700">
          Scoring trigger failed: {trigger.error instanceof Error ? trigger.error.message : "unknown error"}
        </p>
      ) : null}
    </div>
  );
}
