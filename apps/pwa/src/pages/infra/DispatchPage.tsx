import { useQuery } from "@tanstack/react-query";
import { listDispatchJobs } from "../../api/infra";
import {
  Card,
  ErrorState,
  PageHeader,
  Spinner,
  StatusBadge,
  Table,
  Td,
  Th,
} from "../../components/ui";
import { formatDateTime } from "../../lib/format";

/** dispatch-workforce — driver scheduling & dispatch (Temporal workflows). */
export default function DispatchPage() {
  const query = useQuery({
    queryKey: ["infra", "dispatch", "jobs"],
    queryFn: () => listDispatchJobs(),
    refetchInterval: 30_000,
  });

  return (
    <div>
      <PageHeader
        title="Dispatch & Workforce"
        description="Driver shift assignments orchestrated by Temporal workflows (dispatch.job.assigned events)."
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
                <Th>Driver</Th>
                <Th>Vehicle</Th>
                <Th>Route</Th>
                <Th>Starts at</Th>
                <Th>Status</Th>
              </tr>
            </thead>
            <tbody>
              {(query.data ?? []).length === 0 ? (
                <tr>
                  <Td colSpan={5} className="py-10 text-center text-stone-400">
                    No dispatch jobs scheduled.
                  </Td>
                </tr>
              ) : (
                (query.data ?? []).map((job) => (
                  <tr key={job.id} className="hover:bg-surface-sunken/60">
                    <Td className="font-medium text-stone-800">{job.driver_sub.slice(0, 12)}</Td>
                    <Td>{job.vehicle_id ? job.vehicle_id.slice(0, 8) : "—"}</Td>
                    <Td>{job.route}</Td>
                    <Td className="text-stone-500">{formatDateTime(job.starts_at)}</Td>
                    <Td>
                      <StatusBadge status={job.status} />
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
