import { useQuery } from "@tanstack/react-query";
import { Database, ExternalLink } from "lucide-react";
import { listOpenDatasets } from "../../api/citizen";
import {
  Badge,
  Card,
  ErrorState,
  PageHeader,
  Spinner,
  Table,
  Td,
  Th,
} from "../../components/ui";
import { formatDateTime } from "../../lib/format";

/** open-data-portal — GTFS/GTFS-RT feeds and open datasets. */
export default function OpenDataPage() {
  const query = useQuery({
    queryKey: ["citizen", "open-data", "datasets"],
    queryFn: listOpenDatasets,
  });

  return (
    <div>
      <PageHeader
        title="Open Data Portal"
        description="Public GTFS/GTFS-RT feeds and anonymised operational datasets, searchable via OpenSearch."
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
                <Th>Dataset</Th>
                <Th>Kind</Th>
                <Th>Description</Th>
                <Th>Updated</Th>
                <Th className="text-right">Access</Th>
              </tr>
            </thead>
            <tbody>
              {(query.data ?? []).length === 0 ? (
                <tr>
                  <Td colSpan={5} className="py-10 text-center text-stone-400">
                    <Database className="mx-auto mb-2 h-5 w-5 text-stone-300" aria-hidden />
                    No datasets published yet.
                  </Td>
                </tr>
              ) : (
                (query.data ?? []).map((d) => (
                  <tr key={d.id} className="hover:bg-surface-sunken/60">
                    <Td className="font-medium text-stone-800">{d.name}</Td>
                    <Td>
                      <Badge tone="teal">{d.kind}</Badge>
                    </Td>
                    <Td className="max-w-md text-stone-600">{d.description}</Td>
                    <Td className="text-stone-500">{formatDateTime(d.updated_at)}</Td>
                    <Td>
                      <div className="flex justify-end">
                        <a
                          href={d.url}
                          target="_blank"
                          rel="noreferrer"
                          className="inline-flex items-center gap-1.5 rounded-lg border border-stone-300 px-2.5 py-1.5 text-xs font-medium text-stone-600 hover:bg-surface-sunken"
                        >
                          <ExternalLink className="h-3.5 w-3.5" aria-hidden />
                          Open
                        </a>
                      </div>
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
