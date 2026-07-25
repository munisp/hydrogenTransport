import { useEffect, useState } from "react";
import { Link } from "react-router-dom";
import { useQuery } from "@tanstack/react-query";
import { Activity, ArrowLeft, BellRing, Droplet } from "lucide-react";
import {
  getAdminAlerts,
  getAdminHealth,
  getAdminKpis,
  type HealthCheck,
  type OpsAlert,
} from "../../api/admin";
import { cn } from "../../lib/utils";

const REFRESH_MS = 10_000;

/**
 * NOC/SOC "Mission Control" wallboard: chromeless dark full-screen route
 * (/admin/noc) designed for ops-center TVs. Auto-refreshes every 10s.
 */
export default function NocPage() {
  const health = useQuery({
    queryKey: ["admin", "health"],
    queryFn: getAdminHealth,
    refetchInterval: REFRESH_MS,
  });
  const alerts = useQuery({
    queryKey: ["admin", "alerts"],
    queryFn: getAdminAlerts,
    refetchInterval: REFRESH_MS,
  });
  const kpis = useQuery({
    queryKey: ["admin", "kpis"],
    queryFn: getAdminKpis,
    refetchInterval: REFRESH_MS,
  });

  const checks = health.data?.checks ?? [];
  const services = checks.filter((c) => c.kind !== "tcp");
  const middleware = checks.filter((c) => c.kind === "tcp");
  const up = health.data?.summary.up ?? checks.filter((c) => c.status === "up").length;
  const down =
    health.data?.summary.down ?? checks.filter((c) => c.status !== "up").length;

  return (
    <div className="flex min-h-screen flex-col bg-stone-950 px-6 py-5 text-stone-100">
      {/* Header */}
      <header className="flex items-center gap-4">
        <span className="flex h-10 w-10 items-center justify-center rounded-lg bg-amber-600 text-white">
          <Droplet className="h-6 w-6" aria-hidden />
        </span>
        <div>
          <h1 className="text-2xl font-semibold tracking-tight">H2Fleet Mission Control</h1>
          <p className="text-sm text-stone-400">
            {up} up · {down} down · refresh 10s
          </p>
        </div>
        <div className="flex-1" />
        <BigClock />
        <Link
          to="/admin"
          className="ml-4 inline-flex items-center gap-1.5 rounded-lg border border-stone-700 px-3 py-1.5 text-xs text-stone-300 hover:bg-stone-800 focus-visible:outline focus-visible:outline-2 focus-visible:outline-amber-500"
        >
          <ArrowLeft className="h-3.5 w-3.5" aria-hidden /> Console
        </Link>
      </header>

      {/* KPI ticker strip */}
      <div className="mt-5 grid grid-cols-2 gap-3 md:grid-cols-4 xl:grid-cols-6">
        <Ticker label="Vehicles active" value={fmtNum(kpis.data?.fleet?.vehicles_available)} suffix={kpis.data?.fleet ? `/ ${kpis.data.fleet.vehicles_total}` : ""} />
        <Ticker label="Telemetry" value={kpis.data?.fleet ? `${Math.round(kpis.data.fleet.telemetry_points_per_min)}` : "—"} suffix="/min" live />
        <Ticker label="Open incidents" value={fmtNum(kpis.data?.infra?.open_incidents)} alert={(kpis.data?.infra?.open_incidents ?? 0) > 0} />
        <Ticker label="DRT today" value={fmtNum(kpis.data?.citizen?.drt_requests_today)} />
        <Ticker label="Revenue 30d" value={kpis.data?.commerce ? `${Math.round(kpis.data.commerce.revenue_30d_minor / 100).toLocaleString()} ${kpis.data.commerce.currency ?? ""}` : "—"} />
        <Ticker label="Modules on" value={kpis.data?.toggles ? `${kpis.data.toggles.modules_enabled}/${kpis.data.toggles.modules_total}` : "—"} />
      </div>

      <div className="mt-6 grid flex-1 grid-cols-1 gap-6 xl:grid-cols-3">
        {/* Health grid */}
        <section aria-label="Service health" className="xl:col-span-2">
          <SectionTitle icon={<Activity className="h-4 w-4" />} title="Services" />
          <HealthGrid checks={services} loading={health.isLoading} />
          <SectionTitle icon={<Activity className="h-4 w-4" />} title="Middleware" className="mt-6" />
          <HealthGrid checks={middleware} loading={health.isLoading} />
        </section>

        {/* Alerts feed */}
        <section aria-label="Active alerts">
          <SectionTitle icon={<BellRing className="h-4 w-4" />} title={`Active alerts (${alerts.data?.length ?? 0})`} />
          <div className="mt-3 space-y-2 overflow-y-auto" role="feed" aria-busy={alerts.isLoading}>
            {alerts.isLoading ? (
              <p className="text-sm text-stone-500">Loading…</p>
            ) : (alerts.data ?? []).length === 0 ? (
              <div className="rounded-xl border border-emerald-900/60 bg-emerald-950/40 px-4 py-6 text-center">
                <p className="text-lg font-medium text-emerald-400">All clear</p>
                <p className="mt-1 text-xs text-stone-500">No active alerts from Alertmanager.</p>
              </div>
            ) : (
              (alerts.data ?? []).map((a, i) => <AlertCard key={i} alert={a} />)
            )}
          </div>
        </section>
      </div>
    </div>
  );
}

function BigClock() {
  const [now, setNow] = useState(() => new Date());
  useEffect(() => {
    const t = setInterval(() => setNow(new Date()), 1000);
    return () => clearInterval(t);
  }, []);
  return (
    <div className="text-right">
      <p className="font-mono text-4xl font-semibold tabular-nums tracking-tight" aria-live="off">
        {now.toLocaleTimeString(undefined, { hour: "2-digit", minute: "2-digit", second: "2-digit" })}
      </p>
      <p className="text-xs text-stone-500">
        {now.toLocaleDateString(undefined, { weekday: "long", month: "long", day: "numeric" })}
      </p>
    </div>
  );
}

function SectionTitle({ icon, title, className }: { icon: React.ReactNode; title: string; className?: string }) {
  return (
    <h2 className={cn("flex items-center gap-2 text-sm font-semibold uppercase tracking-wider text-stone-400", className)}>
      <span aria-hidden>{icon}</span>
      {title}
    </h2>
  );
}

function Ticker({
  label,
  value,
  suffix,
  live = false,
  alert = false,
}: {
  label: string;
  value: string;
  suffix?: string;
  live?: boolean;
  alert?: boolean;
}) {
  return (
    <div
      className={cn(
        "rounded-xl border px-4 py-3",
        alert ? "border-red-800 bg-red-950/50" : "border-stone-800 bg-stone-900/60",
      )}
    >
      <p className="text-[11px] font-medium uppercase tracking-wider text-stone-500">{label}</p>
      <p className={cn("mt-1 text-2xl font-semibold tabular-nums", alert ? "text-red-400" : "text-stone-100")}>
        {value}
        {suffix ? <span className="ml-1 text-sm font-normal text-stone-500">{suffix}</span> : null}
        {live ? <span className="ml-2 inline-block h-2 w-2 animate-pulse rounded-full bg-emerald-400" aria-label="live" /> : null}
      </p>
    </div>
  );
}

function HealthGrid({ checks, loading }: { checks: HealthCheck[]; loading: boolean }) {
  if (loading && checks.length === 0) {
    return (
      <div className="mt-3 grid grid-cols-2 gap-3 md:grid-cols-3 xl:grid-cols-4">
        {[0, 1, 2, 3].map((i) => (
          <div key={i} className="h-20 animate-pulse rounded-xl bg-stone-900" aria-hidden />
        ))}
      </div>
    );
  }
  return (
    <div className="mt-3 grid grid-cols-2 gap-3 md:grid-cols-3 xl:grid-cols-4">
      {checks.map((c) => {
        const tone =
          c.status === "up" ? "green" : c.status === "degraded" ? "amber" : "red";
        return (
          <div
            key={c.name}
            className={cn(
              "rounded-xl border-l-4 px-4 py-3",
              tone === "green" && "border-emerald-500 bg-emerald-950/30",
              tone === "amber" && "border-amber-500 bg-amber-950/30",
              tone === "red" && "border-red-500 bg-red-950/40",
            )}
            role="status"
            aria-label={`${c.name}: ${c.status}, ${c.latency_ms} milliseconds`}
          >
            <p className="truncate text-sm font-semibold text-stone-100">{c.name}</p>
            <p className="mt-1 flex items-baseline justify-between">
              <span
                className={cn(
                  "text-xs font-medium uppercase",
                  tone === "green" && "text-emerald-400",
                  tone === "amber" && "text-amber-400",
                  tone === "red" && "text-red-400",
                )}
              >
                {c.status}
              </span>
              <span className="font-mono text-xs tabular-nums text-stone-400">{c.latency_ms}ms</span>
            </p>
          </div>
        );
      })}
    </div>
  );
}

function AlertCard({ alert }: { alert: OpsAlert }) {
  const name = alert.labels?.alertname ?? "alert";
  const severity = alert.labels?.severity ?? "info";
  const summary =
    alert.annotations?.summary ?? alert.annotations?.description ?? name;
  const tone =
    severity === "critical" ? "red" : severity === "warning" ? "amber" : "stone";
  return (
    <article
      className={cn(
        "rounded-xl border px-4 py-3",
        tone === "red" && "border-red-800 bg-red-950/40",
        tone === "amber" && "border-amber-800 bg-amber-950/30",
        tone === "stone" && "border-stone-800 bg-stone-900/60",
      )}
    >
      <div className="flex items-center justify-between gap-2">
        <p className="text-sm font-semibold text-stone-100">{name}</p>
        <span
          className={cn(
            "rounded-full px-2 py-0.5 text-[10px] font-semibold uppercase",
            tone === "red" && "bg-red-900/60 text-red-300",
            tone === "amber" && "bg-amber-900/60 text-amber-300",
            tone === "stone" && "bg-stone-800 text-stone-400",
          )}
        >
          {severity}
        </span>
      </div>
      <p className="mt-1 text-xs leading-5 text-stone-400">{summary}</p>
      {alert.startsAt ? (
        <p className="mt-1 font-mono text-[10px] text-stone-600">
          since {new Date(alert.startsAt).toLocaleTimeString()}
        </p>
      ) : null}
    </article>
  );
}

function fmtNum(n: number | null | undefined): string {
  if (n === null || n === undefined) return "—";
  return n.toLocaleString();
}
