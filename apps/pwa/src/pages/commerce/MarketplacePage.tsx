import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Store } from "lucide-react";
import { listMarketplaceOffers, redeemOffer } from "../../api/commerce";
import {
  Badge,
  Button,
  Card,
  CardContent,
  CardHeader,
  CardTitle,
  EmptyState,
  ErrorState,
  PageHeader,
  Spinner,
} from "../../components/ui";
import { formatNumber } from "../../lib/format";

/** loyalty-marketplace — citizen rewards redeemable at local businesses. */
export default function MarketplacePage() {
  const queryClient = useQueryClient();
  const query = useQuery({
    queryKey: ["commerce", "marketplace", "offers"],
    queryFn: listMarketplaceOffers,
  });
  const offers = query.data ?? [];

  const redeem = useMutation({
    mutationFn: redeemOffer,
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ["commerce", "marketplace"] }),
  });

  return (
    <div>
      <PageHeader
        title="Loyalty Marketplace"
        description="Citizens earn mobility points for riding hydrogen buses and redeem them with local businesses."
      />

      {query.isLoading ? (
        <Spinner />
      ) : query.isError ? (
        <ErrorState error={query.error} onRetry={() => query.refetch()} />
      ) : offers.length === 0 ? (
        <EmptyState
          title="No offers published yet"
          body="Local businesses can list rewards here once the marketplace module has active partners."
        />
      ) : (
        <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 xl:grid-cols-3">
          {offers.map((offer) => (
            <Card key={offer.id} className="flex flex-col">
              <CardHeader>
                <div className="flex items-center justify-between gap-2">
                  <CardTitle>{offer.title}</CardTitle>
                  <Badge tone={offer.active ? "green" : "stone"}>
                    {offer.active ? "active" : "inactive"}
                  </Badge>
                </div>
                <p className="flex items-center gap-1.5 text-xs text-stone-500">
                  <Store className="h-3.5 w-3.5" aria-hidden />
                  {offer.business}
                </p>
              </CardHeader>
              <CardContent className="flex flex-1 flex-col justify-between gap-4">
                <p className="text-sm text-stone-600">{offer.description}</p>
                <div className="flex items-center justify-between">
                  <span className="text-sm font-semibold tabular-nums text-accent-muted">
                    {formatNumber(offer.cost_points)} pts
                  </span>
                  <Button
                    variant="secondary"
                    disabled={!offer.active}
                    busy={redeem.isPending && redeem.variables === offer.id}
                    onClick={() => redeem.mutate(offer.id)}
                  >
                    Redeem
                  </Button>
                </div>
              </CardContent>
            </Card>
          ))}
        </div>
      )}
      {redeem.isError ? (
        <p className="mt-4 text-sm text-red-700">
          Redemption failed: {redeem.error instanceof Error ? redeem.error.message : "unknown error"}
        </p>
      ) : null}
    </div>
  );
}
