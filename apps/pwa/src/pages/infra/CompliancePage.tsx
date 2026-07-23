import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { FilePlus2 } from "lucide-react";
import { generateComplianceReport, listComplianceReports } from "../../api/infra";
import {
  Button,
  Card,
  ErrorState,
  PageHeader,
  Spinner,
  Table,
  Td,
  Th,
} from "../../components/ui";
import { formatDateTime } from "../../lib/format";

/** Pretty-print one jsonb report value (nested objects get compact JSON). */
function formatReportValue(value: unknown): string {
  if (value === null || value === undefined) return "—";
  if (typeof value === "object") return JSON.stringify(value);
  return String(value);
}

/** compliance-reporting — regulatory & safety compliance reports. */
export default function CompliancePage() {
  const queryClient = useQueryClient();
  const query = useQuery({
    queryKey: ["infra", "compliance", "reports"],
    queryFn: listComplianceReports,
  });

  const generate = useMutation({
    mutationFn: () => generateComplianceReport(),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ["infra", "compliance"] }),
  });

  return (
    <div>
      <PageHeader
        title="Compliance Reporting"
        description="Regulatory and safety reports generated from incident, telemetry and station records."
        actions={
          <Button onClick={() => generate.mutate()} busy={generate.isPending}>
            <FilePlus2 className="h-4 w-4" aria-hidden />
            Generate safety summary
          </Button>
        }
      />

      {query.isLoading ? (
        <Spinner />
      ) : query.isError ? (
        <ErrorState error={query.error} onRetry={() => query.refetch()} />
      ) : (
        <Card>
          <Table>
            <thead>
              <tr>
                <Th>Report</Th>
                <Th>Generated</Th>
                <Th>Details</Th>
              </tr>
            </thead>
            <tbody>
              {(query.data ?? []).length === 0 ? (
                <tr>
                  <Td colSpan={3} className="py-10 text-center text-stone-400">
                    No reports generated yet.
                  </Td>
                </tr>
              ) : (
                (query.data ?? []).map((r) => (
                  <tr key={r.id} className="align-top hover:bg-surface-sunken/60">
                    <Td className="font-mono text-xs font-medium text-stone-800">
                      {r.id.slice(0, 8)}…
                    </Td>
                    <Td className="whitespace-nowrap text-stone-500">
                      {formatDateTime(r.generated_at)}
                    </Td>
                    <Td>
                      <dl className="space-y-1">
                        {Object.entries(r.report).map(([key, value]) => (
                          <div key={key} className="flex flex-wrap gap-x-2 text-xs">
                            <dt className="font-medium text-stone-600">
                              {key.replace(/_/g, " ")}:
                            </dt>
                            <dd className="break-all text-stone-500">
                              {formatReportValue(value)}
                            </dd>
                          </div>
                        ))}
                      </dl>
                    </Td>
                  </tr>
                ))
              )}
            </tbody>
          </Table>
        </Card>
      )}
    </div>
  );
}
