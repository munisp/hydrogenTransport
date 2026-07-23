/** Display formatters shared across dashboards. */

export function formatNumber(value: number | null | undefined, fractionDigits = 0): string {
  if (value === null || value === undefined || Number.isNaN(value)) return "—";
  return new Intl.NumberFormat("en-US", {
    minimumFractionDigits: fractionDigits,
    maximumFractionDigits: fractionDigits,
  }).format(value);
}

/** Minor units (cents) → localized currency string. */
export function formatMinor(amountMinor: number | null | undefined, currency = "USD"): string {
  if (amountMinor === null || amountMinor === undefined) return "—";
  return new Intl.NumberFormat("en-US", {
    style: "currency",
    currency,
  }).format(amountMinor / 100);
}

export function formatKg(value: number | null | undefined, fractionDigits = 1): string {
  if (value === null || value === undefined) return "—";
  return `${formatNumber(value, fractionDigits)} kg`;
}

export function formatDateTime(iso: string | null | undefined): string {
  if (!iso) return "—";
  const date = new Date(iso);
  if (Number.isNaN(date.getTime())) return "—";
  return date.toLocaleString(undefined, {
    month: "short",
    day: "numeric",
    hour: "2-digit",
    minute: "2-digit",
  });
}

export function formatDate(iso: string | null | undefined): string {
  if (!iso) return "—";
  const date = new Date(iso);
  if (Number.isNaN(date.getTime())) return "—";
  return date.toLocaleDateString(undefined, { year: "numeric", month: "short", day: "numeric" });
}

/** "in 4 min" / "12:05" style arrival countdowns. */
export function formatEta(iso: string, now = Date.now()): string {
  const at = new Date(iso).getTime();
  if (Number.isNaN(at)) return "—";
  const diffMin = Math.round((at - now) / 60_000);
  if (diffMin <= 0) return "due";
  if (diffMin === 1) return "1 min";
  if (diffMin < 60) return `${diffMin} min`;
  return new Date(at).toLocaleTimeString(undefined, { hour: "2-digit", minute: "2-digit" });
}

export function riskTone(score: number): "low" | "medium" | "high" {
  if (score >= 0.75) return "high";
  if (score >= 0.4) return "medium";
  return "low";
}
