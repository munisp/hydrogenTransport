import { useQuery } from "@tanstack/react-query";
import { listDepotBays, listWorkOrders } from "../../api/infra";
import {
  Badge,
  Card,
  CardContent,
  CardHeader,
  CardTitle,
  ErrorState,
  PageHeader,
  Spinner,
  StatusBadge,
  Table,
  Td,
  Th,
} from "../../components/ui";
import { formatDateTime } from "../../lib/format";
import { cn } from "../../lib/utils";

/** depot-management — bays, charging/fueling assets and work orders. */
export default function DepotPage() {
  const bays = useQuery({ queryKey: ["infra", "depot", "bays"], queryFn: listDepotBays, refetchInterval: 30_000 });
  const orders = useQuery({ queryKey: ["infra", "depot", "work-orders"], queryFn: listWorkOrders });

  return (
    <div>
      <PageHeader
        title="Depot Management"
        description="Bay occupancy, fueling/charging assets and the workshop backlog."
      />

      <div className="grid grid-cols-1 gap-4 xl:grid-cols-2">
        <Card>
          <CardHeader>
            <CardTitle>Bays</CardTitle>
          </CardHeader>
          <CardContent>
            {bays.isLoading ? (
              <Spinner />
            ) : bays.isError ? (
              <ErrorState error={bays.error} onRetry={() => bays.refetch()} />
            ) : (
              <div className="grid grid-cols-2 gap-2.5 sm:grid-cols-3">
                {(bays.data ?? []).map((bay) => (
                  <div
                    key={bay.id}
                    className={cn(
                      "rounded-lg border px-3 py-2.5",
                      bay.status === "free"
                        ? "border-teal-200 bg-teal-50"
                        : bay.status === "occupied"
                          ? "border-amber-200 bg-amber-50"
                          : "border-stone-200 bg-stone-100",
                    )}
                  >
                    <div className="flex items-center justify-between gap-2">
                      <span className="text-sm font-medium text-stone-800">{bay.label}</span>
                      <Badge
                        tone={
                          bay.status === "free" ? "teal" : bay.status === "occupied" ? "amber" : "stone"
                        }
                      >
                        {bay.kind}
                      </Badge>
                    </div>
                    <p className="mt-1 text-[11px] text-stone-500">
                      {bay.depot} ·{" "}
                      {bay.occupied_by ? `bus ${bay.occupied_by.slice(0, 8)}` : bay.status.replace(/_/g, " ")}
                    </p>
                  </div>
                ))}
                {(bays.data ?? []).length === 0 ? (
                  <p className="col-span-full py-8 text-center text-sm text-stone-400">
                    No bays configured.
                  </p>
                ) : null}
              </div>
            )}
          </CardContent>
        </Card>

        <Card>
          <CardHeader>
            <CardTitle>Work orders</CardTitle>
          </CardHeader>
          <CardContent className="p-0">
            {orders.isLoading ? (
              <Spinner />
            ) : orders.isError ? (
              <div className="p-5">
                <ErrorState error={orders.error} onRetry={() => orders.refetch()} />
              </div>
            ) : (
              <Table>
                <thead>
                  <tr>
                    <Th>Asset</Th>
                    <Th>Description</Th>
                    <Th>Priority</Th>
                    <Th>Status</Th>
                    <Th>Opened</Th>
                  </tr>
                </thead>
                <tbody>
                  {(orders.data ?? []).length === 0 ? (
                    <tr>
                      <Td colSpan={5} className="py-10 text-center text-stone-400">
                        Workshop backlog is empty.
                      </Td>
                    </tr>
                  ) : (
                    (orders.data ?? []).map((wo) => (
                      <tr key={wo.id} className="hover:bg-surface-sunken/60">
                        <Td className="font-medium text-stone-800">{wo.asset}</Td>
                        <Td className="max-w-64 truncate">{wo.description}</Td>
                        <Td>
                          <Badge tone={wo.priority === "high" ? "red" : wo.priority === "medium" ? "amber" : "stone"}>
                            {wo.priority}
                          </Badge>
                        </Td>
                        <Td>
                          <StatusBadge status={wo.status} />
                        </Td>
                        <Td className="text-stone-500">{formatDateTime(wo.opened_at)}</Td>
                      </tr>
                    ))
                  )}
                </tbody>
              </Table>
            )}
          </CardContent>
        </Card>
      </div>
    </div>
  );
}
