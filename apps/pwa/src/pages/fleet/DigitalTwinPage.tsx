import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { getTwin, listVehicles } from "../../api/fleet";
import {
  Badge,
  Card,
  CardContent,
  CardHeader,
  CardTitle,
  ErrorState,
  Field,
  PageHeader,
  ProgressBar,
  Select,
  Spinner,
  StatusBadge,
} from "../../components/ui";
import { formatDateTime, formatNumber } from "../../lib/format";

/** digital-twin — per-bus twin state from the Rust hot path (Redis + Postgres). */
export default function DigitalTwinPage() {
  const vehicles = useQuery({ queryKey: ["fleet", "vehicles"], queryFn: listVehicles });
  const [busId, setBusId] = useState<string>("");
  const effectiveBusId = busId || vehicles.data?.[0]?.id || "";

  const twin = useQuery({
    queryKey: ["twin", effectiveBusId],
    queryFn: () => getTwin(effectiveBusId),
    enabled: effectiveBusId.length > 0,
    refetchInterval: 5_000,
  });

  const vehicle = vehicles.data?.find((v) => v.id === effectiveBusId);

  return (
    <div>
      <PageHeader
        title="Digital Twin"
        description="Live twin state for any bus — fuel cell, tank, battery and composite health from the twin engine."
      />

      <div className="mb-6 max-w-sm">
        <Field label="Bus">
          <Select value={effectiveBusId} onChange={(e) => setBusId(e.target.value)}>
            {(vehicles.data ?? []).map((v) => (
              <option key={v.id} value={v.id}>
                {v.fleet_no} — {v.model}
              </option>
            ))}
          </Select>
        </Field>
      </div>

      {twin.isLoading ? (
        <Spinner />
      ) : twin.isError || !twin.data ? (
        <ErrorState error={twin.error} onRetry={() => twin.refetch()} />
      ) : (
        <div className="grid grid-cols-1 gap-4 lg:grid-cols-3">
          <Card className="lg:col-span-1">
            <CardHeader>
              <CardTitle>
                {vehicle ? `Bus ${vehicle.fleet_no}` : "Twin"}{" "}
                {vehicle ? <StatusBadge status={vehicle.status} className="ml-2" /> : null}
              </CardTitle>
            </CardHeader>
            <CardContent className="space-y-4">
              <div>
                <div className="mb-1 flex justify-between text-xs text-stone-500">
                  <span>H2 tank</span>
                  <span className="tabular-nums">{formatNumber(twin.data.h2_level_pct, 1)}%</span>
                </div>
                <ProgressBar valuePct={twin.data.h2_level_pct} />
              </div>
              <div>
                <div className="mb-1 flex justify-between text-xs text-stone-500">
                  <span>Battery SoC</span>
                  <span className="tabular-nums">{formatNumber(twin.data.battery_soc_pct, 0)}%</span>
                </div>
                <ProgressBar valuePct={twin.data.battery_soc_pct} tone="teal" />
              </div>
              <div>
                <div className="mb-1 flex justify-between text-xs text-stone-500">
                  <span>Twin status</span>
                  <StatusBadge status={twin.data.status} />
                </div>
              </div>
              <p className="text-[11px] text-stone-400">
                Updated {formatDateTime(twin.data.updated_at)} — refreshes every 5s.
              </p>
            </CardContent>
          </Card>

          <Card className="lg:col-span-2">
            <CardHeader>
              <CardTitle>Powertrain</CardTitle>
            </CardHeader>
            <CardContent>
              <dl className="grid grid-cols-2 gap-6 sm:grid-cols-4">
                <Stat label="Speed" value={`${formatNumber(twin.data.speed_kph, 0)} km/h`} />
                <Stat label="Fuel cell output" value={`${formatNumber(twin.data.fuel_cell_kw, 1)} kW`} />
                <Stat label="Odometer" value={`${formatNumber(twin.data.odometer_km, 0)} km`} />
                <Stat
                  label="Position"
                  value={`${twin.data.lat.toFixed(4)}, ${twin.data.lon.toFixed(4)}`}
                />
              </dl>
              <div className="mt-6">
                <p className="mb-2 text-xs font-medium text-stone-600">Assignment</p>
                <div className="flex flex-wrap gap-1.5">
                  {twin.data.route_id ? <Badge tone="teal">route {twin.data.route_id}</Badge> : null}
                  {twin.data.depot_id ? <Badge tone="stone">depot {twin.data.depot_id}</Badge> : null}
                  {!twin.data.route_id && !twin.data.depot_id ? (
                    <p className="text-sm text-stone-400">No route or depot assignment.</p>
                  ) : null}
                </div>
              </div>
            </CardContent>
          </Card>
        </div>
      )}
    </div>
  );
}

function Stat({ label, value }: { label: string; value: string }) {
  return (
    <div>
      <dt className="text-xs text-stone-500">{label}</dt>
      <dd className="mt-1 text-lg font-semibold tabular-nums text-stone-900">{value}</dd>
    </div>
  );
}
