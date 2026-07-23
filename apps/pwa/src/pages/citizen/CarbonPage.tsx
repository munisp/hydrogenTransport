import { useQuery } from "@tanstack/react-query";
import { Bar, BarChart, CartesianGrid, ResponsiveContainer, Tooltip, XAxis, YAxis } from "recharts";
import { Leaf, Award } from "lucide-react";
import { listCarbonCredits } from "../../api/citizen";
import {
  Card,
  CardContent,
  CardHeader,
  CardTitle,
  ErrorState,
  PageHeader,
  Spinner,
  StatCard,
  Table,
  Td,
  Th,
} from "../../components/ui";
import { formatDate, formatKg, formatNumber } from "../../lib/format";

/** carbon-credits — CO2 avoided accounting and credit issuance history. */
export default function CarbonPage() {
  const query = useQuery({
    queryKey: ["citizen", "carbon", "credits"],
    queryFn: listCarbonCredits,
  });

  const credits = [...(query.data ?? [])].sort((a, b) => a.period.localeCompare(b.period));
  const totalCo2 = credits.reduce((sum, c) => sum + c.kg_co2_avoided, 0);
  const totalCredits = credits.reduce((sum, c) => sum + c.credits, 0);

  return (
    <div>
      <PageHeader
        title="Carbon Credits"
        description="CO2 avoided by the hydrogen fleet versus the diesel baseline, and the credits issued for each accounting period."
      />

      <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
        <StatCard
          label="Total CO2 avoided"
          value={formatKg(totalCo2, 0)}
          hint="All accounting periods"
          icon={<Leaf className="h-4 w-4" />}
        />
        <StatCard
          label="Credits issued"
          value={formatNumber(totalCredits)}
          hint="Verified carbon credits"
          icon={<Award className="h-4 w-4" />}
        />
      </div>

      <div className="mt-6 grid grid-cols-1 gap-4 xl:grid-cols-2">
        <Card>
          <CardHeader>
            <CardTitle>Credits per period</CardTitle>
          </CardHeader>
          <CardContent className="h-72">
            {query.isLoading ? (
              <Spinner />
            ) : query.isError ? (
              <ErrorState error={query.error} onRetry={() => query.refetch()} />
            ) : credits.length === 0 ? (
              <p className="py-16 text-center text-sm text-stone-400">
                No credits issued yet — the carbon-analytics batch job has not run.
              </p>
            ) : (
              <ResponsiveContainer width="100%" height="100%">
                <BarChart data={credits} margin={{ left: 8, right: 8, top: 8 }}>
                  <CartesianGrid strokeDasharray="3 3" stroke="#e7e5e4" vertical={false} />
                  <XAxis dataKey="period" tick={{ fontSize: 11, fill: "#78716c" }} tickLine={false} />
                  <YAxis tick={{ fontSize: 11, fill: "#78716c" }} tickLine={false} axisLine={false} width={40} />
                  <Tooltip contentStyle={{ borderRadius: 12, borderColor: "#e7e5e4", fontSize: 12 }} />
                  <Bar dataKey="credits" name="Credits" fill="#b45309" radius={[4, 4, 0, 0]} />
                </BarChart>
              </ResponsiveContainer>
            )}
          </CardContent>
        </Card>

        <Card>
          <CardHeader>
            <CardTitle>Issuance history</CardTitle>
          </CardHeader>
          <CardContent className="p-0">
            <Table>
              <thead>
                <tr>
                  <Th>Period</Th>
                  <Th className="text-right">CO2 avoided</Th>
                  <Th className="text-right">Credits</Th>
                  <Th>Issued</Th>
                </tr>
              </thead>
              <tbody>
                {credits.length === 0 ? (
                  <tr>
                    <Td colSpan={4} className="py-10 text-center text-stone-400">
                      Nothing issued yet.
                    </Td>
                  </tr>
                ) : (
                  [...credits].reverse().map((c) => (
                    <tr key={c.id} className="hover:bg-surface-sunken/60">
                      <Td className="font-medium text-stone-800">{c.period}</Td>
                      <Td className="text-right tabular-nums">{formatKg(c.kg_co2_avoided, 0)}</Td>
                      <Td className="text-right tabular-nums">{formatNumber(c.credits)}</Td>
                      <Td className="text-stone-500">{formatDate(c.issued_at)}</Td>
                    </tr>
                  ))
                )}
              </tbody>
            </Table>
          </CardContent>
        </Card>
      </div>
    </div>
  );
}
