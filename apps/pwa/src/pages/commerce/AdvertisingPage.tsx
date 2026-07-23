import { useQuery } from "@tanstack/react-query";
import { listAdCampaigns } from "../../api/commerce";
import {
  Card,
  ErrorState,
  PageHeader,
  ProgressBar,
  Spinner,
  StatusBadge,
  Table,
  Td,
  Th,
} from "../../components/ui";
import { formatDate, formatMinor, formatNumber } from "../../lib/format";

/** advertising — on-bus / digital ad inventory & campaigns. */
export default function AdvertisingPage() {
  const query = useQuery({ queryKey: ["commerce", "ads"], queryFn: listAdCampaigns });
  const campaigns = query.data ?? [];

  return (
    <div>
      <PageHeader
        title="Advertising"
        description="Campaign delivery across bus exterior, interior, station screen and PWA placements."
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
                <Th>Campaign</Th>
                <Th>Advertiser</Th>
                <Th>Placement</Th>
                <Th className="text-right">Impressions</Th>
                <Th className="w-40">Flight</Th>
                <Th className="text-right">Budget</Th>
                <Th>Status</Th>
              </tr>
            </thead>
            <tbody>
              {campaigns.length === 0 ? (
                <tr>
                  <Td colSpan={7} className="py-10 text-center text-stone-400">
                    No campaigns booked yet.
                  </Td>
                </tr>
              ) : (
                campaigns.map((c) => {
                  const start = new Date(c.starts_at).getTime();
                  const end = new Date(c.ends_at).getTime();
                  const pct =
                    Number.isFinite(start) && Number.isFinite(end) && end > start
                      ? ((Date.now() - start) / (end - start)) * 100
                      : 0;
                  return (
                    <tr key={c.id} className="hover:bg-surface-sunken/60">
                      <Td className="font-medium text-stone-800">{c.name}</Td>
                      <Td>{c.advertiser}</Td>
                      <Td>{c.placement.replace(/_/g, " ")}</Td>
                      <Td className="text-right tabular-nums">{formatNumber(c.impressions)}</Td>
                      <Td>
                        <ProgressBar valuePct={pct} tone="teal" />
                        <p className="mt-1 text-[11px] text-stone-500">
                          {formatDate(c.starts_at)} → {formatDate(c.ends_at)}
                        </p>
                      </Td>
                      <Td className="text-right tabular-nums">
                        {formatMinor(c.budget_minor, c.currency)}
                      </Td>
                      <Td>
                        <StatusBadge status={c.status} />
                      </Td>
                    </tr>
                  );
                })
              )}
            </tbody>
          </Table>
        </Card>
      )}
    </div>
  );
}
