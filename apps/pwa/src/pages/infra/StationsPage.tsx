import { useQuery } from "@tanstack/react-query";
import { listStations } from "../../api/infra";
import {
  Card,
  CardContent,
  CardHeader,
  CardTitle,
  ErrorState,
  PageHeader,
  ProgressBar,
  Spinner,
  StatusBadge,
} from "../../components/ui";
import { formatKg, formatNumber } from "../../lib/format";

/** refueling-stations — station status, queue and H2 inventory gauges. */
export default function StationsPage() {
  const query = useQuery({
    queryKey: ["infra", "stations"],
    queryFn: listStations,
    refetchInterval: 30_000,
  });

  return (
    <div>
      <PageHeader
        title="Refueling Stations"
        description="Inventory, status and queue depth at each hydrogen refueling station."
      />

      {query.isLoading ? (
        <Spinner />
      ) : query.isError ? (
        <ErrorState error={query.error} onRetry={() => query.refetch()} />
      ) : (
        <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 xl:grid-cols-3">
          {(query.data ?? []).map((s) => {
            const pct = s.capacity_kg > 0 ? (s.available_kg / s.capacity_kg) * 100 : 0;
            return (
              <Card key={s.id}>
                <CardHeader>
                  <div className="flex items-start justify-between gap-2">
                    <CardTitle>{s.name}</CardTitle>
                    <StatusBadge status={s.status} />
                  </div>
                </CardHeader>
                <CardContent className="space-y-4">
                  <div>
                    <div className="mb-1 flex justify-between text-xs text-stone-500">
                      <span>Available H2</span>
                      <span className="tabular-nums">
                        {formatKg(s.available_kg, 0)} / {formatKg(s.capacity_kg, 0)}
                      </span>
                    </div>
                    <ProgressBar valuePct={pct} />
                  </div>
                  <dl className="grid grid-cols-2 gap-3 text-sm">
                    <div>
                      <dt className="text-xs text-stone-500">Fill level</dt>
                      <dd className="font-medium tabular-nums text-stone-800">
                        {formatNumber(pct, 0)}%
                      </dd>
                    </div>
                    <div>
                      <dt className="text-xs text-stone-500">Queue</dt>
                      <dd className="font-medium tabular-nums text-stone-800">
                        {s.queue_length ?? 0} bus{(s.queue_length ?? 0) === 1 ? "" : "es"}
                      </dd>
                    </div>
                  </dl>
                  {pct < 25 ? (
                    <p className="rounded-lg bg-amber-50 px-3 py-2 text-xs text-amber-800 ring-1 ring-inset ring-amber-200">
                      Low inventory — schedule an electrolyser delivery or throttle refueling slots.
                    </p>
                  ) : null}
                </CardContent>
              </Card>
            );
          })}
          {(query.data ?? []).length === 0 ? (
            <p className="col-span-full py-10 text-center text-sm text-stone-400">
              No stations registered in infra-api.
            </p>
          ) : null}
        </div>
      )}
    </div>
  );
}
