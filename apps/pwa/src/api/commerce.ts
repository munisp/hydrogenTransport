import { API_PREFIX } from "../config";
import { apiFetch, buildQuery, unwrap, unwrapList } from "./client";
import type {
  AdCampaign,
  EnergyTrade,
  FarePayment,
  GovKpis,
  MarketplaceOffer,
} from "./types";

/** Commerce & Finance domain (commerce-api). */

export async function getGovKpis(): Promise<GovKpis> {
  const raw = await apiFetch<unknown>(`${API_PREFIX.commerce}/v1/gov/kpis`);
  return unwrap<GovKpis>(raw);
}

export async function listFarePayments(
  params: { status?: string } = {},
): Promise<FarePayment[]> {
  const raw = await apiFetch<unknown>(
    `${API_PREFIX.commerce}/v1/payments${buildQuery({ status: params.status })}`,
  );
  return unwrapList<FarePayment>(raw);
}

export async function listMarketplaceOffers(): Promise<MarketplaceOffer[]> {
  const raw = await apiFetch<unknown>(`${API_PREFIX.commerce}/v1/marketplace/offers`);
  return unwrapList<MarketplaceOffer>(raw);
}

export interface RedeemResult {
  redeemed_offer_id: string;
  points_spent: number;
  remaining_points: number;
}

export async function redeemOffer(id: string): Promise<RedeemResult> {
  return apiFetch<RedeemResult>(`${API_PREFIX.commerce}/v1/loyalty/redeem`, {
    method: "POST",
    body: JSON.stringify({ offer_id: id }),
  });
}

export async function listEnergyTrades(): Promise<EnergyTrade[]> {
  const raw = await apiFetch<unknown>(`${API_PREFIX.commerce}/v1/energy/trades`);
  return unwrapList<EnergyTrade>(raw);
}

export async function createEnergyTrade(body: {
  kind: string;
  quantity_kg: number;
  price_minor: number;
}): Promise<EnergyTrade> {
  return apiFetch<EnergyTrade>(`${API_PREFIX.commerce}/v1/energy/trades`, {
    method: "POST",
    body: JSON.stringify(body),
  });
}

export async function listAdCampaigns(): Promise<AdCampaign[]> {
  const raw = await apiFetch<unknown>(`${API_PREFIX.commerce}/v1/ads/campaigns`);
  return unwrapList<AdCampaign>(raw);
}
