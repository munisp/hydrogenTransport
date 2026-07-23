import { useState, type FormEvent } from "react";
import { useMutation } from "@tanstack/react-query";
import { Route as RouteIcon } from "lucide-react";
import { requestOptimization } from "../../api/fleet";
import type { OptimizationPlan } from "../../api/types";
import {
  Badge,
  Button,
  Card,
  CardContent,
  CardHeader,
  CardTitle,
  Field,
  Input,
  PageHeader,
} from "../../components/ui";
import { formatKg, formatNumber } from "../../lib/format";

/** route-energy-optimizer — OR-Tools route + refueling schedule optimization. */
export default function RouteOptimizerPage() {
  const [busIds, setBusIds] = useState("");
  const [date, setDate] = useState(() => new Date().toISOString().slice(0, 10));
  const [plan, setPlan] = useState<OptimizationPlan | null>(null);

  const optimize = useMutation({
    mutationFn: requestOptimization,
    onSuccess: setPlan,
  });

  function submit(e: FormEvent) {
    e.preventDefault();
    if (!date) return;
    const ids = busIds
      .split(",")
      .map((s) => s.trim())
      .filter(Boolean);
    optimize.mutate({ bus_ids: ids.length > 0 ? ids : null, date });
  }

  return (
    <div>
      <PageHeader
        title="Route & Energy Optimizer"
        description="OR-Tools optimization of bus assignments and refueling stops against station capacity and route energy demand."
      />

      <div className="grid grid-cols-1 gap-4 xl:grid-cols-3">
        <Card>
          <CardHeader>
            <CardTitle>Optimize the fleet</CardTitle>
          </CardHeader>
          <CardContent>
            <form className="space-y-4" onSubmit={submit}>
              <Field label="Bus IDs (optional)" hint="Comma-separated UUIDs; empty = all buses with telemetry">
                <Input
                  value={busIds}
                  onChange={(e) => setBusIds(e.target.value)}
                  placeholder="all buses"
                />
              </Field>
              <Field label="Service date">
                <Input type="date" required value={date} onChange={(e) => setDate(e.target.value)} />
              </Field>
              <Button type="submit" busy={optimize.isPending} className="w-full">
                <RouteIcon className="h-4 w-4" aria-hidden />
                Optimize
              </Button>
              {optimize.isError ? (
                <p className="text-xs text-red-700">
                  {optimize.error instanceof Error ? optimize.error.message : "Optimization failed"}
                </p>
              ) : null}
            </form>
          </CardContent>
        </Card>

        <Card className="xl:col-span-2">
          <CardHeader>
            <CardTitle>{plan ? `Plan for ${plan.date}` : "Latest plan"}</CardTitle>
            {plan ? (
              <p className="text-xs text-stone-500">
                solver {plan.solver_status} · data source {plan.data_source} ·{" "}
                {plan.plans.length} bus{plan.plans.length === 1 ? "" : "es"} planned
              </p>
            ) : null}
          </CardHeader>
          <CardContent>
            {!plan ? (
              <p className="py-10 text-center text-sm text-stone-400">
                No plan yet — submit an optimization request to generate one.
              </p>
            ) : (
              <div className="space-y-6">
                {plan.unassigned_stops.length > 0 ? (
                  <div className="rounded-xl border border-amber-200 bg-amber-50 px-4 py-3 text-sm text-amber-800">
                    {plan.unassigned_stops.length} stop
                    {plan.unassigned_stops.length === 1 ? "" : "s"} could not be assigned:{" "}
                    {plan.unassigned_stops.join(", ")}
                  </div>
                ) : null}

                <dl className="grid grid-cols-2 gap-6 sm:grid-cols-3">
                  <div>
                    <dt className="text-xs text-stone-500">Buses planned</dt>
                    <dd className="mt-1 text-lg font-semibold tabular-nums text-stone-900">
                      {plan.plans.length}
                    </dd>
                  </div>
                  <div>
                    <dt className="text-xs text-stone-500">Infeasible</dt>
                    <dd className="mt-1 text-lg font-semibold tabular-nums text-stone-900">
                      {plan.plans.filter((p) => !p.feasible).length}
                    </dd>
                  </div>
                  <div>
                    <dt className="text-xs text-stone-500">Refuel stops</dt>
                    <dd className="mt-1 text-lg font-semibold tabular-nums text-stone-900">
                      {plan.plans.reduce((n, p) => n + p.refuels.length, 0)}
                    </dd>
                  </div>
                </dl>

                <div className="space-y-4">
                  {plan.plans.map((bp) => (
                    <div
                      key={bp.bus_id}
                      className="rounded-lg border border-stone-200 bg-surface-sunken p-3"
                    >
                      <div className="flex flex-wrap items-center justify-between gap-2">
                        <p className="text-sm font-medium text-stone-800">
                          {bp.fleet_no || bp.bus_id.slice(0, 8)}
                        </p>
                        <div className="flex items-center gap-2">
                          {!bp.feasible ? <Badge tone="red">infeasible</Badge> : null}
                          <Badge tone="teal">{formatNumber(bp.total_route_km, 1)} km</Badge>
                          <Badge tone="stone">
                            H2 {formatKg(bp.h2_start_kg)} → {formatKg(bp.h2_end_kg)}
                          </Badge>
                        </div>
                      </div>

                      {bp.notes.length > 0 ? (
                        <p className="mt-1 text-xs text-stone-500">{bp.notes.join("; ")}</p>
                      ) : null}

                      {bp.refuels.length > 0 ? (
                        <ul className="mt-2 space-y-1.5">
                          {bp.refuels.map((r, i) => (
                            <li
                              key={`${r.station_id}-${i}`}
                              className="flex items-center justify-between rounded-lg bg-white px-3 py-1.5 text-xs text-stone-600"
                            >
                              <span>
                                Refuel at {r.station_name || r.station_id} (after stop #
                                {r.at_stop_sequence})
                              </span>
                              <span className="flex items-center gap-2 text-stone-500">
                                <span>range before {formatNumber(r.remaining_range_km_before, 0)} km</span>
                                <Badge tone="teal">{formatKg(r.kg_taken)}</Badge>
                              </span>
                            </li>
                          ))}
                        </ul>
                      ) : (
                        <p className="mt-2 text-xs text-stone-400">
                          No refueling required for this bus.
                        </p>
                      )}

                      {bp.legs.length > 0 ? (
                        <p className="mt-2 text-[11px] text-stone-400">
                          {bp.legs.length} stops ·{" "}
                          {bp.legs
                            .slice(0, 4)
                            .map((l) => l.stop_name)
                            .join(" → ")}
                          {bp.legs.length > 4 ? " → …" : ""}
                        </p>
                      ) : null}
                    </div>
                  ))}
                </div>
              </div>
            )}
          </CardContent>
        </Card>
      </div>
    </div>
  );
}
