import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { listFarePayments } from "../../api/commerce";
import {
  Card,
  ErrorState,
  PageHeader,
  Select,
  Spinner,
  StatusBadge,
  Table,
  Td,
  Th,
} from "../../components/ui";
import { formatDateTime, formatMinor } from "../../lib/format";

const STATUS_FILTERS = ["", "initiated", "settled", "failed", "refunded"] as const;

/** fare-payments — fare collection over Mojaloop rails (TigerBeetle ledger). */
export default function PaymentsPage() {
  const [status, setStatus] = useState<string>("");
  const query = useQuery({
    queryKey: ["commerce", "payments", status],
    queryFn: () => listFarePayments({ status: status || undefined }),
  });
  const payments = query.data ?? [];

  return (
    <div>
      <PageHeader
        title="Fare Payments"
        description="Rider fare collection settled over Mojaloop rails; double-entry positions held in the TigerBeetle ledger."
        actions={
          <div className="w-44">
            <Select value={status} onChange={(e) => setStatus(e.target.value)} aria-label="Filter by status">
              {STATUS_FILTERS.map((s) => (
                <option key={s} value={s}>
                  {s === "" ? "All statuses" : s}
                </option>
              ))}
            </Select>
          </div>
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
                <Th>Payment</Th>
                <Th>Rider</Th>
                <Th className="text-right">Amount</Th>
                <Th>Mojaloop transfer</Th>
                <Th>Status</Th>
                <Th>Created</Th>
              </tr>
            </thead>
            <tbody>
              {payments.length === 0 ? (
                <tr>
                  <Td colSpan={6} className="py-10 text-center text-stone-400">
                    No payments match this filter.
                  </Td>
                </tr>
              ) : (
                payments.map((p) => (
                  <tr key={p.id} className="hover:bg-surface-sunken/60">
                    <Td className="font-mono text-xs">{p.id.slice(0, 8)}…</Td>
                    <Td className="font-mono text-xs">{p.rider_sub.slice(0, 12)}…</Td>
                    <Td className="text-right tabular-nums">{formatMinor(p.amount_minor, p.currency)}</Td>
                    <Td className="font-mono text-xs text-stone-500">
                      {p.mojaloop_transfer_id ?? "—"}
                    </Td>
                    <Td>
                      <StatusBadge status={p.status} />
                    </Td>
                    <Td className="text-stone-500">{formatDateTime(p.created_at)}</Td>
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
