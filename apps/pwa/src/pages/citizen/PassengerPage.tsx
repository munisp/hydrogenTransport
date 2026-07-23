import { useState, type FormEvent } from "react";
import { useQuery } from "@tanstack/react-query";
import { ArrowRight, Siren } from "lucide-react";
import { listArrivals, listServiceAlerts, listStops, planJourney } from "../../api/citizen";
import type { JourneyOption } from "../../api/types";
import {
  Badge,
  Button,
  Card,
  CardContent,
  CardHeader,
  CardTitle,
  ErrorState,
  Field,
  PageHeader,
  Select,
  Spinner,
} from "../../components/ui";
import { formatEta, formatNumber } from "../../lib/format";

/** passenger-pwa — arrivals board, journey planner and service alerts. */
export default function PassengerPage() {
  return (
    <div>
      <PageHeader
        title="Passenger Information"
        description="Live arrivals, journey planning and service alerts for hydrogen bus riders."
      />
      <div className="grid grid-cols-1 gap-4 xl:grid-cols-2">
        <ArrivalsBoard />
        <div className="space-y-4">
          <JourneyPlanner />
          <ServiceAlerts />
        </div>
      </div>
    </div>
  );
}

function ArrivalsBoard() {
  const stops = useQuery({ queryKey: ["citizen", "stops"], queryFn: listStops });
  const [stopId, setStopId] = useState("");
  const effectiveStop = stopId || stops.data?.[0]?.stop_id || "";

  const arrivals = useQuery({
    queryKey: ["citizen", "arrivals", effectiveStop],
    queryFn: () => listArrivals(effectiveStop),
    enabled: effectiveStop.length > 0,
    refetchInterval: 20_000,
  });

  const stopName = stops.data?.find((s) => s.stop_id === effectiveStop)?.stop_name;

  return (
    <Card>
      <CardHeader>
        <CardTitle>Arrivals {stopName ? `— ${stopName}` : ""}</CardTitle>
      </CardHeader>
      <CardContent className="space-y-4">
        <Field label="Stop">
          <Select value={effectiveStop} onChange={(e) => setStopId(e.target.value)}>
            {(stops.data ?? []).map((s) => (
              <option key={s.stop_id} value={s.stop_id}>
                {s.stop_name}
              </option>
            ))}
          </Select>
        </Field>

        {arrivals.isLoading ? (
          <Spinner className="py-6" />
        ) : arrivals.isError ? (
          <ErrorState error={arrivals.error} onRetry={() => arrivals.refetch()} />
        ) : (arrivals.data ?? []).length === 0 ? (
          <p className="py-6 text-center text-sm text-stone-400">
            No upcoming departures at this stop.
          </p>
        ) : (
          <ul className="divide-y divide-stone-100">
            {(arrivals.data ?? []).map((a, i) => (
              <li key={`${a.route_id}-${a.scheduled_at}-${i}`} className="flex items-center gap-3 py-2.5">
                <span className="flex h-9 w-12 items-center justify-center rounded-lg bg-teal-soft text-sm font-semibold text-teal-accent">
                  {a.route_short_name}
                </span>
                <div className="min-w-0 flex-1">
                  <p className="truncate text-sm font-medium text-stone-800">{a.headsign}</p>
                  <p className="text-[11px] text-stone-500">
                    {a.in_minutes <= 0 ? "due now" : `in ${formatNumber(a.in_minutes, 0)} min`}
                  </p>
                </div>
                <span className="text-sm font-semibold tabular-nums text-stone-800">
                  {formatEta(a.scheduled_at)}
                </span>
              </li>
            ))}
          </ul>
        )}
      </CardContent>
    </Card>
  );
}

function JourneyPlanner() {
  const stops = useQuery({ queryKey: ["citizen", "stops"], queryFn: listStops });
  const [from, setFrom] = useState("");
  const [to, setTo] = useState("");
  const [options, setOptions] = useState<JourneyOption[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const stopList = stops.data ?? [];
  const effectiveFrom = from || stopList[0]?.stop_id || "";
  const effectiveTo = to || stopList[1]?.stop_id || stopList[0]?.stop_id || "";
  const stopName = new Map(stopList.map((s) => [s.stop_id, s.stop_name]));

  async function submit(e: FormEvent) {
    e.preventDefault();
    if (!effectiveFrom || !effectiveTo) return;
    setBusy(true);
    setError(null);
    try {
      setOptions(await planJourney({ from: effectiveFrom, to: effectiveTo }));
    } catch (err) {
      setError(err instanceof Error ? err.message : "Journey planning failed");
    } finally {
      setBusy(false);
    }
  }

  return (
    <Card>
      <CardHeader>
        <CardTitle>Journey planner</CardTitle>
      </CardHeader>
      <CardContent className="space-y-4">
        <form className="flex flex-wrap items-end gap-3" onSubmit={submit}>
          <div className="min-w-36 flex-1">
            <Field label="From">
              <Select value={effectiveFrom} onChange={(e) => setFrom(e.target.value)}>
                {stopList.map((s) => (
                  <option key={s.stop_id} value={s.stop_id}>
                    {s.stop_name}
                  </option>
                ))}
              </Select>
            </Field>
          </div>
          <div className="min-w-36 flex-1">
            <Field label="To">
              <Select value={effectiveTo} onChange={(e) => setTo(e.target.value)}>
                {stopList.map((s) => (
                  <option key={s.stop_id} value={s.stop_id}>
                    {s.stop_name}
                  </option>
                ))}
              </Select>
            </Field>
          </div>
          <Button type="submit" busy={busy}>
            Plan
          </Button>
        </form>

        {error ? <p className="text-xs text-red-700">{error}</p> : null}

        {options && options.length === 0 ? (
          <p className="py-4 text-center text-sm text-stone-400">
            No direct routes serve both stops.
          </p>
        ) : null}

        {options?.map((opt) => (
          <div
            key={`${opt.route_id}-${opt.depart_at}`}
            className="rounded-lg border border-stone-200 bg-surface-sunken p-3"
          >
            <div className="flex items-center justify-between">
              <p className="text-sm font-medium text-stone-800">
                Line {opt.route_short_name} · {formatNumber(opt.duration_min, 0)} min
              </p>
              <Badge tone="teal">{formatEta(opt.depart_at)}</Badge>
            </div>
            <p className="mt-2 flex items-center gap-2 text-xs text-stone-600">
              <Badge tone="teal">bus</Badge>
              <span className="truncate">
                {stopName.get(opt.from_stop_id) ?? opt.from_stop_id}{" "}
                <ArrowRight className="inline h-3 w-3" aria-hidden />{" "}
                {stopName.get(opt.to_stop_id) ?? opt.to_stop_id}
              </span>
              <span className="text-stone-400">({opt.route_id})</span>
            </p>
          </div>
        ))}
      </CardContent>
    </Card>
  );
}

function ServiceAlerts() {
  const alerts = useQuery({
    queryKey: ["citizen", "alerts"],
    queryFn: listServiceAlerts,
    refetchInterval: 60_000,
  });

  return (
    <Card>
      <CardHeader>
        <CardTitle>Service alerts</CardTitle>
      </CardHeader>
      <CardContent className="space-y-3">
        {alerts.isLoading ? (
          <Spinner className="py-6" />
        ) : alerts.isError ? (
          <ErrorState error={alerts.error} onRetry={() => alerts.refetch()} />
        ) : (alerts.data ?? []).length === 0 ? (
          <p className="py-4 text-center text-sm text-stone-400">
            No active alerts — services running normally.
          </p>
        ) : (
          (alerts.data ?? []).map((alert) => (
            <div key={alert.id} className="flex gap-3 rounded-lg border border-stone-200 px-3 py-2.5">
              <Siren
                className={`mt-0.5 h-4 w-4 shrink-0 ${
                  alert.severity === "severe" ? "text-red-600" : "text-amber-600"
                }`}
                aria-hidden
              />
              <div>
                <p className="text-sm font-medium text-stone-800">{alert.header}</p>
                <p className="mt-0.5 text-xs text-stone-600">{alert.description}</p>
                {alert.route_ids && alert.route_ids.length > 0 ? (
                  <p className="mt-1 text-[11px] text-stone-400">
                    Routes: {alert.route_ids.join(", ")}
                  </p>
                ) : null}
              </div>
            </div>
          ))
        )}
      </CardContent>
    </Card>
  );
}
