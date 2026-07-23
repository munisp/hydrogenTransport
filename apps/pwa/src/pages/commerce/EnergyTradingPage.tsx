import { useState, type FormEvent } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { createEnergyTrade, listEnergyTrades } from "../../api/commerce";
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
  Select,
  Spinner,
  StatusBadge,
  Table,
  Td,
  Th,
} from "../../components/ui";
import { formatDateTime, formatKg, formatMinor } from "../../lib/format";

/** energy-trading — surplus H2 / energy trading ledger. */
export default function EnergyTradingPage() {
  const queryClient = useQueryClient();
  const query = useQuery({ queryKey: ["commerce", "trades"], queryFn: listEnergyTrades });
  const trades = query.data ?? [];

  const [kind, setKind] = useState("h2_sale");
  const [quantity, setQuantity] = useState("");
  const [price, setPrice] = useState("");

  const create = useMutation({
    mutationFn: createEnergyTrade,
    onSuccess: () => {
      setQuantity("");
      setPrice("");
      void queryClient.invalidateQueries({ queryKey: ["commerce", "trades"] });
    },
  });

  function submit(e: FormEvent) {
    e.preventDefault();
    const qty = Number(quantity);
    const priceMajor = Number(price);
    if (!Number.isFinite(qty) || qty <= 0 || !Number.isFinite(priceMajor) || priceMajor <= 0) return;
    create.mutate({ kind, quantity_kg: qty, price_minor: Math.round(priceMajor * 100) });
  }

  return (
    <div>
      <PageHeader
        title="Energy Trading"
        description="Sell surplus electrolyser hydrogen or electricity back to the market; positions settle in the TigerBeetle ENERGY_TRADE accounts."
      />

      <div className="grid grid-cols-1 gap-4 xl:grid-cols-3">
        <Card className="xl:col-span-1">
          <CardHeader>
            <CardTitle>New trade</CardTitle>
          </CardHeader>
          <CardContent>
            <form className="space-y-4" onSubmit={submit}>
              <Field label="Instrument">
                <Select value={kind} onChange={(e) => setKind(e.target.value)}>
                  <option value="h2_sale">Sell H2</option>
                  <option value="h2_purchase">Buy H2</option>
                  <option value="electricity">Electricity</option>
                </Select>
              </Field>
              <Field label="Quantity (kg)">
                <Input
                  type="number"
                  min="0"
                  step="0.1"
                  required
                  value={quantity}
                  onChange={(e) => setQuantity(e.target.value)}
                  placeholder="250"
                />
              </Field>
              <Field label="Total price" hint="Major currency units; stored as minor units on the ledger.">
                <Input
                  type="number"
                  min="0"
                  step="0.01"
                  required
                  value={price}
                  onChange={(e) => setPrice(e.target.value)}
                  placeholder="1750.00"
                />
              </Field>
              <Button type="submit" busy={create.isPending} className="w-full">
                Post trade
              </Button>
              {create.isError ? (
                <p className="text-xs text-red-700">
                  {create.error instanceof Error ? create.error.message : "Trade rejected"}
                </p>
              ) : null}
            </form>
          </CardContent>
        </Card>

        <Card className="xl:col-span-2">
          <CardHeader>
            <CardTitle>Trade ledger</CardTitle>
          </CardHeader>
          <CardContent className="p-0">
            {query.isLoading ? (
              <Spinner />
            ) : query.isError ? (
              <div className="p-5">
                <ErrorState error={query.error} onRetry={() => query.refetch()} />
              </div>
            ) : (
              <Table>
                <thead>
                  <tr>
                    <Th>Trade</Th>
                    <Th>Kind</Th>
                    <Th className="text-right">Quantity</Th>
                    <Th className="text-right">Price</Th>
                    <Th>Status</Th>
                    <Th>Created</Th>
                  </tr>
                </thead>
                <tbody>
                  {trades.length === 0 ? (
                    <tr>
                      <Td colSpan={6} className="py-10 text-center text-stone-400">
                        No trades recorded yet.
                      </Td>
                    </tr>
                  ) : (
                    trades.map((t) => (
                      <tr key={t.id} className="hover:bg-surface-sunken/60">
                        <Td className="font-mono text-xs">{t.id.slice(0, 8)}…</Td>
                        <Td>{t.kind.replace(/_/g, " ")}</Td>
                        <Td className="text-right tabular-nums">{formatKg(t.quantity_kg)}</Td>
                        <Td className="text-right tabular-nums">{formatMinor(t.price_minor)}</Td>
                        <Td>
                          <StatusBadge status={t.status} />
                        </Td>
                        <Td className="text-stone-500">{formatDateTime(t.created_at)}</Td>
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
