import { useState } from "react";
import { useMutation, useQuery } from "@tanstack/react-query";
import { BrainCircuit, FlaskConical, TrendingUp } from "lucide-react";
import {
  MODEL_LABELS,
  listDrift,
  listModels,
  scoreMaintenance,
  synthesizeMaintenanceWindow,
  type MaintenanceScoreResult,
} from "../../api/mlplatform";
import {
  Badge,
  Button,
  Card,
  CardContent,
  CardHeader,
  CardTitle,
  EmptyState,
  ErrorState,
  Field,
  Input,
  ProgressBar,
  Skeleton,
  StatusBadge,
  Table,
  Td,
  Th,
} from "../../components/ui";

/**
 * AI Lab: model registry (GET /v1/ml/models), feature-drift status
 * (GET /v1/ml/drift) and a "score a bus" demo panel against the maintenance
 * LSTM (POST /v1/ml/maintenance/score).
 */
export default function AiLabTab() {
  return (
    <div className="space-y-10">
      <ModelRegistry />
      <DriftPanel />
      <ScoreBusPanel />
    </div>
  );
}

// ---- Model registry --------------------------------------------------------------

function ModelRegistry() {
  const models = useQuery({
    queryKey: ["ml", "models"],
    queryFn: listModels,
    refetchInterval: 60_000,
  });

  return (
    <section aria-labelledby="ai-registry">
      <div className="mb-3">
        <h2 id="ai-registry" className="text-sm font-semibold text-stone-800">
          Model registry
        </h2>
        <p className="text-xs text-stone-500">
          Champion/challenger variants served by the ml-platform (PyTorch, CPU). A/B
          assignment is deterministic per subject.
        </p>
      </div>

      {models.isLoading ? (
        <Card className="p-5" aria-busy="true">
          <div className="space-y-3">
            {[0, 1, 2, 3, 4].map((i) => (
              <Skeleton key={i} className="h-9 w-full" />
            ))}
          </div>
        </Card>
      ) : models.isError ? (
        <ErrorState error={models.error} onRetry={() => models.refetch()} />
      ) : (
        <Card>
          <Table>
            <thead>
              <tr>
                <Th>Model</Th>
                <Th>Champion</Th>
                <Th>Challenger</Th>
                <Th>Key metrics</Th>
                <Th>Trained</Th>
                <Th>Status</Th>
              </tr>
            </thead>
            <tbody>
              {models.data?.map((m) => {
                const champion = m.champion;
                const challenger = m.challenger;
                return (
                  <tr key={m.name}>
                    <Td>
                      <p className="font-medium text-stone-800">{MODEL_LABELS[m.name]}</p>
                      <p className="font-mono text-[10px] text-stone-400">{m.name} · pytorch</p>
                    </Td>
                    <Td className="whitespace-nowrap">
                      {champion ? (
                        <>
                          <Badge tone="green">v{champion.version}</Badge>
                          {typeof champion.n_params === "number" ? (
                            <span className="ml-1 text-[10px] text-stone-400">
                              {(champion.n_params / 1000).toFixed(0)}k params
                            </span>
                          ) : null}
                        </>
                      ) : (
                        "—"
                      )}
                    </Td>
                    <Td className="whitespace-nowrap">
                      {challenger ? <Badge tone="amber">v{challenger.version}</Badge> : "—"}
                    </Td>
                    <Td>
                      <div className="flex flex-wrap gap-1">
                        {Object.entries(champion?.metrics ?? {})
                          .filter(([, v]) => typeof v === "number")
                          .slice(0, 4)
                          .map(([k, v]) => (
                            <Badge key={k} tone="stone" className="font-mono text-[10px]">
                              {k}={typeof v === "number" ? v.toFixed(3) : String(v)}
                            </Badge>
                          ))}
                      </div>
                    </Td>
                    <Td className="whitespace-nowrap text-xs">
                      {champion?.trained_at
                        ? new Date(champion.trained_at).toLocaleDateString()
                        : "—"}
                    </Td>
                    <Td>
                      <StatusBadge status={m.loaded ? "online" : "offline"} />
                    </Td>
                  </tr>
                );
              })}
            </tbody>
          </Table>
        </Card>
      )}
    </section>
  );
}

// ---- Drift ------------------------------------------------------------------------

function DriftPanel() {
  const drift = useQuery({
    queryKey: ["ml", "drift"],
    queryFn: listDrift,
    refetchInterval: 60_000,
  });

  return (
    <section aria-labelledby="ai-drift">
      <div className="mb-3">
        <h2 id="ai-drift" className="text-sm font-semibold text-stone-800">
          Feature drift
        </h2>
        <p className="text-xs text-stone-500">
          PSI / KS vs training baselines. PSI &gt; 0.2 flags a significant input shift.
        </p>
      </div>

      {drift.isLoading ? (
        <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 xl:grid-cols-3" aria-busy="true">
          {[0, 1, 2].map((i) => (
            <Card key={i} className="p-5">
              <Skeleton className="h-4 w-32" />
              <Skeleton className="mt-3 h-8 w-full" />
            </Card>
          ))}
        </div>
      ) : drift.isError ? (
        <ErrorState error={drift.error} onRetry={() => drift.refetch()} />
      ) : (
        <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 xl:grid-cols-3">
          {drift.data?.map((d) => (
            <Card key={d.model} className="p-5">
              <div className="flex items-center justify-between gap-3">
                <p className="text-sm font-medium text-stone-800">{MODEL_LABELS[d.model]}</p>
                <StatusBadge status={d.status} />
              </div>
              <div className="mt-3">
                <div className="flex items-baseline justify-between text-xs text-stone-500">
                  <span>Worst PSI</span>
                  <span className="font-mono text-stone-700">
                    {d.worst_psi == null ? "—" : d.worst_psi.toFixed(3)}
                  </span>
                </div>
                <ProgressBar
                  className="mt-1.5"
                  valuePct={d.worst_psi == null ? 0 : Math.min(100, (d.worst_psi / 0.4) * 100)}
                  tone={d.status === "drift" ? "red" : d.worst_psi != null && d.worst_psi > 0.1 ? "amber" : "teal"}
                />
              </div>
              <p className="mt-2 text-[11px] text-stone-400">
                {typeof d.n_observed === "number"
                  ? `${d.n_observed} observed rows`
                  : "awaiting inference traffic"}
              </p>
              {Object.keys(d.features).length > 0 ? (
                <details className="mt-2 text-xs">
                  <summary className="cursor-pointer text-stone-500 hover:text-stone-700">
                    Per-feature PSI / KS
                  </summary>
                  <ul className="mt-1 space-y-0.5 font-mono text-[11px] text-stone-500">
                    {Object.entries(d.features).map(([f, r]) => (
                      <li key={f} className={r.drifted ? "font-semibold text-red-700" : ""}>
                        {f}: psi={r.psi.toFixed(3)} ks={r.ks.toFixed(3)}
                      </li>
                    ))}
                  </ul>
                </details>
              ) : null}
            </Card>
          ))}
        </div>
      )}
    </section>
  );
}

// ---- Score a bus ------------------------------------------------------------------

function ScoreBusPanel() {
  const [busId, setBusId] = useState("H2-007");
  const [result, setResult] = useState<MaintenanceScoreResult | null>(null);

  const score = useMutation({
    mutationFn: () => scoreMaintenance(busId.trim(), synthesizeMaintenanceWindow(48)),
    onSuccess: setResult,
  });

  return (
    <section aria-labelledby="ai-score">
      <div className="mb-3">
        <h2 id="ai-score" className="text-sm font-semibold text-stone-800">
          Score a bus
        </h2>
        <p className="text-xs text-stone-500">
          Runs the maintenance LSTM over a synthesised 48-step telemetry window
          (live windows are plumbed in a later iteration) and returns per-component
          failure risk.
        </p>
      </div>

      <Card>
        <CardHeader>
          <CardTitle className="flex items-center gap-2">
            <FlaskConical className="h-4 w-4 text-accent" aria-hidden /> Maintenance risk demo
          </CardTitle>
        </CardHeader>
        <CardContent>
          <div className="flex flex-wrap items-end gap-3">
            <div className="w-44">
              <Field label="Bus (fleet no or UUID)">
                <Input value={busId} onChange={(e) => setBusId(e.target.value)} placeholder="H2-007" />
              </Field>
            </div>
            <Button busy={score.isPending} disabled={busId.trim().length === 0} onClick={() => score.mutate()}>
              <TrendingUp className="h-4 w-4" aria-hidden /> Score
            </Button>
          </div>

          {score.isError ? (
            <div className="mt-4">
              <ErrorState error={score.error} onRetry={() => score.mutate()} />
            </div>
          ) : null}

          {result ? (
            <div className="mt-5">
              <p className="mb-2 text-xs text-stone-500">
                variant <Badge tone="teal">{result.variant}</Badge> · model v{result.model_version}
              </p>
              <ul className="space-y-2">
                {result.predictions.map((p) => (
                  <li key={p.component}>
                    <div className="flex items-baseline justify-between text-xs">
                      <span className="font-medium capitalize text-stone-700">
                        {p.component.replace(/_/g, " ")}
                      </span>
                      <span className="font-mono text-stone-500">
                        risk {(p.risk_score * 100).toFixed(1)}% · ~{p.days_to_failure.toFixed(0)}d
                      </span>
                    </div>
                    <ProgressBar className="mt-1" valuePct={p.risk_score * 100} />
                  </li>
                ))}
              </ul>
            </div>
          ) : !score.isError ? (
            <div className="mt-5">
              <EmptyState
                icon={<BrainCircuit className="h-6 w-6" />}
                title="No score yet"
                body="Enter a fleet number and run the model to see component risk scores."
              />
            </div>
          ) : null}
        </CardContent>
      </Card>
    </section>
  );
}
