import { useState, type FormEvent } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { createDrtRequest, listDrtRequests } from "../../api/citizen";
import {
  Button,
  Card,
  CardContent,
  CardHeader,
  CardTitle,
  ErrorState,
  Field,
  Input,
  PageHeader,
  Spinner,
  StatusBadge,
  Table,
  Td,
  Th,
} from "../../components/ui";
import { formatDateTime } from "../../lib/format";

/** Scalar lat/lon fields are omitempty on the backend; render a dash when absent. */
function formatCoord(value: number | null | undefined): string {
  return typeof value === "number" ? value.toFixed(4) : "—";
}

/** demand-responsive — on-demand shuttle requests (drt.requested events). */
export default function DrtPage() {
  const queryClient = useQueryClient();
  const requests = useQuery({
    queryKey: ["citizen", "drt", "requests"],
    queryFn: listDrtRequests,
    refetchInterval: 20_000,
  });

  const [pickupLat, setPickupLat] = useState("");
  const [pickupLon, setPickupLon] = useState("");
  const [dropLat, setDropLat] = useState("");
  const [dropLon, setDropLon] = useState("");

  const create = useMutation({
    mutationFn: createDrtRequest,
    onSuccess: () => {
      setPickupLat("");
      setPickupLon("");
      setDropLat("");
      setDropLon("");
      void queryClient.invalidateQueries({ queryKey: ["citizen", "drt"] });
    },
  });

  function submit(e: FormEvent) {
    e.preventDefault();
    const pLat = Number(pickupLat);
    const pLon = Number(pickupLon);
    const dLat = Number(dropLat);
    const dLon = Number(dropLon);
    if (![pLat, pLon, dLat, dLon].every(Number.isFinite)) return;
    create.mutate({
      pickup: { lat: pLat, lon: pLon },
      dropoff: { lat: dLat, lon: dLon },
    });
  }

  return (
    <div>
      <PageHeader
        title="Demand-Responsive Transport"
        description="Book an on-demand hydrogen shuttle within the service zone; dispatch matches requests to spare capacity."
      />

      <div className="grid grid-cols-1 gap-4 xl:grid-cols-3">
        <Card>
          <CardHeader>
            <CardTitle>Request a shuttle</CardTitle>
          </CardHeader>
          <CardContent>
            <form className="space-y-4" onSubmit={submit}>
              <div className="grid grid-cols-2 gap-3">
                <Field label="Pickup latitude">
                  <Input required inputMode="decimal" value={pickupLat} onChange={(e) => setPickupLat(e.target.value)} placeholder="50.0755" />
                </Field>
                <Field label="Pickup longitude">
                  <Input required inputMode="decimal" value={pickupLon} onChange={(e) => setPickupLon(e.target.value)} placeholder="14.4378" />
                </Field>
                <Field label="Dropoff latitude">
                  <Input required inputMode="decimal" value={dropLat} onChange={(e) => setDropLat(e.target.value)} placeholder="50.0875" />
                </Field>
                <Field label="Dropoff longitude">
                  <Input required inputMode="decimal" value={dropLon} onChange={(e) => setDropLon(e.target.value)} placeholder="14.4210" />
                </Field>
              </div>
              <Button type="submit" busy={create.isPending} className="w-full">
                Request pickup
              </Button>
              {create.isError ? (
                <p className="text-xs text-red-700">
                  {create.error instanceof Error ? create.error.message : "Request failed"}
                </p>
              ) : null}
            </form>
          </CardContent>
        </Card>

        <Card className="xl:col-span-2">
          <CardHeader>
            <CardTitle>Recent requests</CardTitle>
          </CardHeader>
          <CardContent className="p-0">
            {requests.isLoading ? (
              <Spinner />
            ) : requests.isError ? (
              <div className="p-5">
                <ErrorState error={requests.error} onRetry={() => requests.refetch()} />
              </div>
            ) : (
              <Table>
                <thead>
                  <tr>
                    <Th>Request</Th>
                    <Th>Pickup</Th>
                    <Th>Dropoff</Th>
                    <Th>Status</Th>
                    <Th>Requested</Th>
                  </tr>
                </thead>
                <tbody>
                  {(requests.data ?? []).length === 0 ? (
                    <tr>
                      <Td colSpan={5} className="py-10 text-center text-stone-400">
                        No DRT requests yet.
                      </Td>
                    </tr>
                  ) : (
                    (requests.data ?? []).map((r) => (
                      <tr key={r.id} className="hover:bg-surface-sunken/60">
                        <Td className="font-mono text-xs">{r.id.slice(0, 8)}…</Td>
                        <Td className="tabular-nums text-xs">
                          {formatCoord(r.pickup_lat)}, {formatCoord(r.pickup_lon)}
                        </Td>
                        <Td className="tabular-nums text-xs">
                          {formatCoord(r.dropoff_lat)}, {formatCoord(r.dropoff_lon)}
                        </Td>
                        <Td>
                          <StatusBadge status={r.status} />
                        </Td>
                        <Td className="text-stone-500">{formatDateTime(r.requested_at)}</Td>
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
