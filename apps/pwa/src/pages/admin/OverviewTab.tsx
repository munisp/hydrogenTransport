import { useQuery } from "@tanstack/react-query";
import {
  AlertTriangle,
  Bus,
  CreditCard,
  Leaf,
  Radio,
  ShieldAlert,
  Users,
  Wallet,
} from "lucide-react";
import { getAdminKpis, type AdminKpis } from "../../api/admin";
import {
  Badge,
  Card,
  CardContent,
  CardHeader,
  CardTitle,
  ErrorState,
  Skeleton,
} from "../../components/ui";
import { cn } from "../../lib/utils";

/** Overview tab: cross-domain KPI cards from GET /v1/admin/kpis + module enablement. */
export default function OverviewTab() {
  const kpis = useQuery({
    queryKey: ["admin", "kpis"],
    queryFn: getAdminKpis,
    refetchInterval: 30_000,
  });

  if (kpis.isLoading) return <OverviewSkeleton />;
  if (kpis.isError) return <ErrorState error={kpis.error} onRetry={() => kpis.refetch()} />;
  const data = kpis.data;
  if (!data) return null;

  const degraded = data.meta?.degraded ?? [];

  return (
    <div className="space-y-8">
      {degraded.length > 0 ? (
        <div className="flex items-start gap-2 rounded-xl border border-amber-200 bg-amber-50 px-4 py-3 text-sm text-amber-900" role="status">
          <AlertTriangle className="mt-0.5 h-4 w-4 shrink-0" aria-hidden />
          <p>
            Partial data — these sources are unreachable:{" "}
            <span className="font-medium">{degraded.join(", ")}</span>. Affected cards
            show “—” until the services recover.
          </p>
        </div>
      ) : null}

      <section aria-labelledby="ov-fleet">
        <SectionHeading id="ov-fleet" title="Fleet" hint="telematics & vehicles" />
        <KpiGrid>
          <KpiCard
            icon={<Bus className="h-4 w-4" />}
            label="Vehicles total"
            value={fmt(data.fleet?.vehicles_total)}
            degraded={!data.fleet}
          />
          <KpiCard
            icon={<Bus className="h-4 w-4" />}
            label="Vehicles available"
            value={fmt(data.fleet?.vehicles_available)}
            degraded={!data.fleet}
          />
          <KpiCard
            icon={<Radio className="h-4 w-4" />}
            label="Telemetry rate"
            value={data.fleet ? `${fmt(Math.round(data.fleet.telemetry_points_per_min))}/min` : "—"}
            degraded={!data.fleet}
            wide
          />
        </KpiGrid>
      </section>

      <section aria-labelledby="ov-safety">
        <SectionHeading id="ov-safety" title="Safety" hint="incidents & leak detection" />
        <KpiGrid>
          <KpiCard
            icon={<ShieldAlert className="h-4 w-4" />}
            label="Open incidents"
            value={fmt(data.infra?.open_incidents)}
            degraded={!data.infra}
            tone={data.infra && data.infra.open_incidents > 0 ? "amber" : "default"}
          />
        </KpiGrid>
      </section>

      <section aria-labelledby="ov-citizen">
        <SectionHeading id="ov-citizen" title="Citizen" hint="DRT & carbon" />
        <KpiGrid>
          <KpiCard
            icon={<Users className="h-4 w-4" />}
            label="DRT requests today"
            value={fmt(data.citizen?.drt_requests_today)}
            degraded={!data.citizen}
          />
          <KpiCard
            icon={<Leaf className="h-4 w-4" />}
            label="CO₂ avoided (total)"
            value={data.citizen ? `${fmt(Math.round(data.citizen.carbon_kg_co2_total))} kg` : "—"}
            degraded={!data.citizen}
          />
        </KpiGrid>
      </section>

      <section aria-labelledby="ov-commerce">
        <SectionHeading id="ov-commerce" title="Commerce" hint="fares & revenue, 30 days" />
        <KpiGrid>
          <KpiCard
            icon={<CreditCard className="h-4 w-4" />}
            label="Settled payments (30d)"
            value={fmt(data.commerce?.payments_30d)}
            degraded={!data.commerce}
          />
          <KpiCard
            icon={<Wallet className="h-4 w-4" />}
            label="Revenue (30d)"
            value={
              data.commerce
                ? `${fmt(Math.round(data.commerce.revenue_30d_minor / 100))} ${data.commerce.currency ?? ""}`.trim()
                : "—"
            }
            degraded={!data.commerce}
          />
        </KpiGrid>
      </section>

      <section aria-labelledby="ov-modules">
        <SectionHeading id="ov-modules" title="Modules" hint="feature-toggle enablement" />
        <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 xl:grid-cols-4">
          <Card className="p-5">
            <p className="text-xs font-medium uppercase tracking-wide text-stone-500">Enabled</p>
            <p className="mt-2 text-2xl font-semibold tabular-nums text-stone-900">
              {data.toggles ? `${data.toggles.modules_enabled} / ${data.toggles.modules_total}` : "—"}
            </p>
          </Card>
          {Object.entries(data.toggles?.domains ?? {}).map(([domain, counts]) => (
            <Card key={domain} className="p-5">
              <p className="text-xs font-medium uppercase tracking-wide text-stone-500">{domain}</p>
              <p className="mt-2 text-2xl font-semibold tabular-nums text-stone-900">
                {counts.enabled} / {counts.total}
              </p>
              <p className="mt-1 text-xs text-stone-500">modules on</p>
            </Card>
          ))}
        </div>
      </section>

      <p className="text-xs text-stone-400">
        Generated {new Date(data.generated_at).toLocaleString()} · refreshes every 30s
        {data.meta?.partial ? " · partial" : ""}
      </p>
    </div>
  );
}

function SectionHeading({ id, title, hint }: { id: string; title: string; hint: string }) {
  return (
    <div className="mb-3 flex items-baseline gap-2">
      <h2 id={id} className="text-sm font-semibold text-stone-800">{title}</h2>
      <span className="text-xs text-stone-400">{hint}</span>
    </div>
  );
}

function KpiGrid({ children }: { children: React.ReactNode }) {
  return <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 xl:grid-cols-3">{children}</div>;
}

/**
 * KPI card with a sparkline-ready footer band: once per-hour history endpoints
 * land, drop a series into the reserved strip without re-laying out the card.
 */
function KpiCard({
  icon,
  label,
  value,
  degraded,
  tone = "default",
  wide = false,
}: {
  icon: React.ReactNode;
  label: string;
  value: string;
  degraded?: boolean;
  tone?: "default" | "amber";
  wide?: boolean;
}) {
  return (
    <Card className={cn("flex flex-col p-5", wide && "sm:col-span-2 xl:col-span-1")}>
      <div className="flex items-start justify-between gap-3">
        <div>
          <p className="text-xs font-medium uppercase tracking-wide text-stone-500">{label}</p>
          <p
            className={cn(
              "mt-2 text-2xl font-semibold tabular-nums",
              degraded ? "text-stone-300" : tone === "amber" ? "text-amber-700" : "text-stone-900",
            )}
          >
            {value}
          </p>
        </div>
        <div className="rounded-lg bg-accent-soft p-2 text-accent" aria-hidden>{icon}</div>
      </div>
      <div className="mt-4 flex h-8 items-end border-t border-dashed border-stone-100 pt-2">
        {degraded ? <Badge tone="amber">degraded</Badge> : null}
      </div>
    </Card>
  );
}

function fmt(n: number | null | undefined): string {
  if (n === null || n === undefined || Number.isNaN(n)) return "—";
  return n.toLocaleString();
}

function OverviewSkeleton() {
  return (
    <div className="space-y-8" aria-busy="true" aria-label="Loading KPIs">
      {[0, 1, 2].map((s) => (
        <div key={s}>
          <Skeleton className="mb-3 h-4 w-32" />
          <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 xl:grid-cols-3">
            {[0, 1, 2].map((c) => (
              <Card key={c} className="p-5">
                <CardHeader className="p-0">
                  <CardTitle>
                    <Skeleton className="h-3 w-24" />
                  </CardTitle>
                </CardHeader>
                <CardContent className="p-0 pt-3">
                  <Skeleton className="h-7 w-20" />
                  <Skeleton className="mt-4 h-8 w-full" />
                </CardContent>
              </Card>
            ))}
          </div>
        </div>
      ))}
    </div>
  );
}
