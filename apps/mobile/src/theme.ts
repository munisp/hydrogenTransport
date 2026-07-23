/** Warm, low-saturation palette shared with the PWA (SPEC §3.7). */
export const colors = {
  bg: "#fafaf9",
  card: "#ffffff",
  sunken: "#f5f5f4",
  border: "#e7e5e4",
  text: "#1c1917",
  textMuted: "#78716c",
  textFaint: "#a8a29e",
  accent: "#b45309", // amber-700
  accentSoft: "#fef3c7",
  teal: "#0f766e",
  tealSoft: "#ccfbf1",
  red: "#b91c1c",
  redSoft: "#fee2e2",
  amberSoft: "#fef3c7",
} as const;

export type StatusTone = "green" | "amber" | "red" | "teal" | "stone";

export function statusTone(status: string): StatusTone {
  switch (status) {
    case "in_service":
    case "online":
    case "active":
    case "settled":
    case "completed":
    case "resolved":
    case "accepted":
    case "matched":
      return "green";
    case "assigned":
    case "requested":
    case "in_progress":
    case "acknowledged":
    case "open":
    case "en_route":
      return "amber";
    case "failed":
    case "critical":
    case "offline":
    case "cancelled":
      return "red";
    case "executed":
    case "issued":
      return "teal";
    default:
      return "stone";
  }
}
