import { useQuery } from "@tanstack/react-query";
import { listFuelReadings, listVehicles } from "../../api/fleet";
import {
  Card,
  EnergyTypeBadge,
  energyUnit,
  ErrorState,
  PageHeader,
  ProgressBar,
  Spinner,
  Table,
  Td,
  Th,
} from "../../components/ui";
import { formatDateTime, formatNumber } from "../../lib/format";

/** fuel-monitoring — energy levels (H2/battery/diesel/CNG), consumption and range prediction. */
export default function FuelMonitoringPage() {
  const readings = useQuery({ queryKey: ["fleet", "fuel", "readings"], queryFn: listFuelReadings, refetchInterval: 30_000 });
  const vehicles = useQuery({ queryKey: ["fleet", "vehicles"], queryFn: listVehicles });

  const fleetNo = new Map((vehicles.data ?? []).map((v) => [v.id, v.fleet_no]));
  const vehicleEnergyType = new Map((vehicles.data ?? []).map((v) => [v.id, v.energy_type]));
  const levelPct = (r: { energy_level_pct?: number; h2_level_pct: number }) =>
    r.energy_level_pct ?? r.h2_level_pct;
  const low = (readings.data ?? []).filter((r) => levelPct(r) < 20).length;

  return (
    <div>
      <PageHeader
        title="Fuel Monitoring"
        description="Tank levels, specific consumption and predicted range across the fleet."
      />

      {low > 0 ? (
        <div className="mb-4 rounded-xl border border-amber-200 bg-amber-50 px-4 py-3 text-sm text-amber-800">
          {low} bus{low === 1 ? "" : "es"} below 20% energy — prioritize refueling dispatch.
        </div>
      ) : null}

      {readings.isLoading ? (
        <Spinner />
      ) : readings.isError ? (
        <ErrorState error={readings.error} onRetry={() => readings.refetch()} />
      ) : (
        <Card>
          <Table>
            <thead>
              <tr>
                <Th>Bus</Th>
                <Th className="w-64">Tank level</Th>
                <Th className="text-right">Onboard energy</Th>
                <Th className="text-right">Estimated range</Th>
                <Th>Last reading</Th>
              </tr>
            </thead>
            <tbody>
              {(readings.data ?? []).length === 0 ? (
                <tr>
                  <Td colSpan={5} className="py-10 text-center text-stone-400">
                    No fuel readings yet — telemetry ingestion has not produced any samples.
                  </Td>
                </tr>
              ) : (
                [...(readings.data ?? [])]
                  .sort((a, b) => levelPct(a) - levelPct(b))
                  .map((r) => {
                    const energyType = r.energy_type ?? vehicleEnergyType.get(r.bus_id);
                    return (
                    <tr key={r.bus_id} className="hover:bg-surface-sunken/60">
                      <Td className="font-medium text-stone-800">
                        <div className="flex items-center gap-2">
                          {r.fleet_no || fleetNo.get(r.bus_id) || r.bus_id.slice(0, 8)}
                          <EnergyTypeBadge energyType={energyType} />
                        </div>
                      </Td>
                      <Td>
                        <div className="flex items-center gap-2">
                          <ProgressBar valuePct={levelPct(r)} className="w-40" />
                          <span className="text-xs tabular-nums text-stone-500">
                            {formatNumber(levelPct(r), 1)}%
                          </span>
                        </div>
                      </Td>
                      <Td className="text-right tabular-nums">
                        {formatNumber(r.energy_remaining ?? r.h2_remaining_kg, 1)} {energyUnit(energyType)}
                      </Td>
                      <Td className="text-right tabular-nums">
                        {formatNumber(r.estimated_range_km, 0)} km
                      </Td>
                      <Td className="text-stone-500">{formatDateTime(r.measured_at)}</Td>
                    </tr>
                    );
                  })
              )}
            </tbody>
          </Table>
        </Card>
      )}
    </div>
  );
}
