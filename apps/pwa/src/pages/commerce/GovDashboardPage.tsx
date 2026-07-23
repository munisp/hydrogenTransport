import { useQuery } from "@tanstack/react-query";
import {
  Bar,
  BarChart,
  CartesianGrid,
  ResponsiveContainer,
  Tooltip,
  XAxis,
  YAxis,
} from "recharts";
import { Activity, AlertTriangle, Bus, Coins, Fuel, Leaf, Users } from "lucide-react";
import { getGovKpis } from "../../api/commerce";
import { listCarbonCredits } from "../../api/citizen";
import { Card, CardContent, CardHeader, CardTitle, ErrorState, PageHeader, Spinner, StatCard } from "../../components/ui";
import { formatKg, formatMinor, formatNumber } from "../../lib/format";

/** gov-dashboard — city KPI dashboard (cost, emissions, ridership, uptime). */
export default function GovDashboardPage() {
  const query = useQuery({ queryKey: ["commerce", "kpis"], queryFn: getGovKpis });
  const credits = useQuery({
    queryKey: ["citizen", "carbon", "credits"],
    queryFn: listCarbonCredits,
  });

  if (query.isLoading) return <Spinner />;
  if (query.isError || !query.data) {
    return <ErrorState error={query.error} onRetry={() => query.refetch()} />;
  }
  const kpis = query.data;
  const creditSeries = [...(credits.data ?? [])]
    .sort((a, b) => a.period.localeCompare(b.period))
    .map((c) => ({ date: c.period, value: c.credits }));

  return (
    <div>
      <PageHeader
        title="City Dashboard"
        description="Fleet KPIs — uptime, hydrogen, emissions, ridership and fare revenue."
      />

      <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 xl:grid-cols-3">
        <StatCard
          label="Fleet uptime"
          value={`${formatNumber(kpis.fleet_uptime_pct, 1)}%`}
          hint={`${kpis.vehicles_active} of ${kpis.vehicles_total} vehicles active`}
          icon={<Activity className="h-4 w-4" />}
        />
        <StatCard
          label="CO2 avoided"
          value={formatKg(kpis.kg_co2_avoided_total, 0)}
          hint="Versus diesel baseline"
          icon={<Leaf className="h-4 w-4" />}
        />
        <StatCard
          label="Carbon credits"
          value={formatNumber(kpis.carbon_credits_total)}
          hint="Issued credits, all periods"
          icon={<Leaf className="h-4 w-4" />}
        />
        <StatCard
          label="Ridership (30d)"
          value={formatNumber(kpis.ridership_estimate_30d)}
          hint="Estimated boardings"
          icon={<Users className="h-4 w-4" />}
        />
        <StatCard
          label="Fare revenue (30d)"
          value={formatMinor(kpis.revenue_30d_minor)}
          hint={`${formatNumber(kpis.settled_payments_30d)} settled payments via Mojaloop rails`}
          icon={<Coins className="h-4 w-4" />}
        />
        <StatCard
          label="H2 available"
          value={formatKg(kpis.stations_available_kg, 0)}
          hint="Across refueling stations"
          icon={<Fuel className="h-4 w-4" />}
        />
        <StatCard
          label="Vehicles"
          value={`${kpis.vehicles_active}/${kpis.vehicles_total}`}
          hint="Active / total fleet"
          icon={<Bus className="h-4 w-4" />}
        />
        <StatCard
          label="Open incidents"
          value={formatNumber(kpis.open_incidents)}
          hint="Leak-detection & safety workflow"
          icon={<AlertTriangle className="h-4 w-4" />}
        />
      </div>

      <div className="mt-6 grid grid-cols-1 gap-4">
        <Card>
          <CardHeader>
            <CardTitle>Carbon credits issued</CardTitle>
          </CardHeader>
          <CardContent className="h-72">
            {credits.isLoading ? (
              <Spinner className="py-16" />
            ) : creditSeries.length === 0 ? (
              <p className="py-16 text-center text-sm text-stone-400">
                No carbon credit periods issued yet.
              </p>
            ) : (
              <ResponsiveContainer width="100%" height="100%">
                <BarChart data={creditSeries} margin={{ left: 8, right: 8, top: 8 }}>
                  <CartesianGrid strokeDasharray="3 3" stroke="#e7e5e4" vertical={false} />
                  <XAxis dataKey="date" tick={{ fontSize: 11, fill: "#78716c" }} tickLine={false} />
                  <YAxis tick={{ fontSize: 11, fill: "#78716c" }} tickLine={false} axisLine={false} width={56} />
                  <Tooltip
                    contentStyle={{ borderRadius: 12, borderColor: "#e7e5e4", fontSize: 12 }}
                  />
                  <Bar dataKey="value" name="credits" fill="#0f766e" radius={[4, 4, 0, 0]} />
                </BarChart>
              </ResponsiveContainer>
            )}
          </CardContent>
        </Card>
      </div>
    </div>
  );
}
