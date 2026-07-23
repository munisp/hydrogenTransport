import React, { type ReactNode } from "react";
import {
  ActivityIndicator,
  StyleSheet,
  Text,
  TouchableOpacity,
  View,
  type GestureResponderEvent,
} from "react-native";
import { colors, statusTone, type StatusTone } from "../theme";

/** Compact UI kit in the same warm palette as the PWA. */

export function Screen({ children }: { children: ReactNode }) {
  return <View style={styles.screen}>{children}</View>;
}

export function ScreenTitle({ title, subtitle }: { title: string; subtitle?: string }) {
  return (
    <View style={styles.titleWrap}>
      <Text style={styles.title}>{title}</Text>
      {subtitle ? <Text style={styles.subtitle}>{subtitle}</Text> : null}
    </View>
  );
}

export function Card({ children, style }: { children: ReactNode; style?: object }) {
  return <View style={[styles.card, style]}>{children}</View>;
}

const toneColors: Record<StatusTone, { bg: string; fg: string }> = {
  green: { bg: "#ecfdf5", fg: "#065f46" },
  amber: { bg: "#fffbeb", fg: "#92400e" },
  red: { bg: colors.redSoft, fg: colors.red },
  teal: { bg: colors.tealSoft, fg: colors.teal },
  stone: { bg: colors.sunken, fg: colors.textMuted },
};

export function StatusBadge({ status }: { status: string }) {
  const tone = toneColors[statusTone(status)];
  return (
    <View style={[styles.badge, { backgroundColor: tone.bg }]}>
      <Text style={[styles.badgeText, { color: tone.fg }]}>{status}</Text>
    </View>
  );
}

export function Button({
  label,
  onPress,
  variant = "primary",
  busy = false,
  disabled = false,
}: {
  label: string;
  onPress: (e: GestureResponderEvent) => void;
  variant?: "primary" | "secondary" | "danger";
  busy?: boolean;
  disabled?: boolean;
}) {
  const bg =
    variant === "primary" ? colors.accent : variant === "danger" ? colors.red : colors.card;
  const fg = variant === "secondary" ? colors.text : "#ffffff";
  return (
    <TouchableOpacity
      style={[
        styles.button,
        { backgroundColor: bg },
        variant === "secondary" && styles.buttonSecondary,
        (disabled || busy) && styles.buttonDisabled,
      ]}
      onPress={onPress}
      disabled={disabled || busy}
      activeOpacity={0.8}
    >
      {busy ? (
        <ActivityIndicator color={fg} size="small" />
      ) : (
        <Text style={[styles.buttonText, { color: fg }]}>{label}</Text>
      )}
    </TouchableOpacity>
  );
}

export function Loading() {
  return (
    <View style={styles.centered}>
      <ActivityIndicator color={colors.textMuted} size="large" />
    </View>
  );
}

export function Notice({
  title,
  body,
  tone = "stone",
}: {
  title: string;
  body?: string;
  tone?: StatusTone;
}) {
  const palette = toneColors[tone];
  return (
    <View style={[styles.notice, { backgroundColor: palette.bg }]}>
      <Text style={[styles.noticeTitle, { color: palette.fg }]}>{title}</Text>
      {body ? <Text style={[styles.noticeBody, { color: palette.fg }]}>{body}</Text> : null}
    </View>
  );
}

export function ErrorNotice({ error }: { error: unknown }) {
  return (
    <Notice
      tone="red"
      title="Couldn't load data"
      body={
        error instanceof Error
          ? `${error.message} — is APISIX reachable at the configured apiBase?`
          : "Unknown error"
      }
    />
  );
}

export function formatEta(iso: string): string {
  const at = new Date(iso).getTime();
  if (Number.isNaN(at)) return "—";
  const diffMin = Math.round((at - Date.now()) / 60_000);
  if (diffMin <= 0) return "due";
  if (diffMin < 60) return `${diffMin} min`;
  return new Date(at).toLocaleTimeString(undefined, { hour: "2-digit", minute: "2-digit" });
}

export function formatDateTime(iso: string): string {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return "—";
  return d.toLocaleString(undefined, { month: "short", day: "numeric", hour: "2-digit", minute: "2-digit" });
}

const styles = StyleSheet.create({
  screen: { flex: 1, backgroundColor: colors.bg, padding: 16 },
  titleWrap: { marginBottom: 16, marginTop: 8 },
  title: { fontSize: 22, fontWeight: "700", color: colors.text },
  subtitle: { marginTop: 4, fontSize: 13, color: colors.textMuted },
  card: {
    backgroundColor: colors.card,
    borderRadius: 14,
    borderWidth: 1,
    borderColor: colors.border,
    padding: 14,
    marginBottom: 12,
  },
  badge: { borderRadius: 999, paddingHorizontal: 8, paddingVertical: 3, alignSelf: "flex-start" },
  badgeText: { fontSize: 11, fontWeight: "600" },
  button: {
    borderRadius: 10,
    paddingVertical: 12,
    paddingHorizontal: 16,
    alignItems: "center",
    justifyContent: "center",
  },
  buttonSecondary: { borderWidth: 1, borderColor: colors.border },
  buttonDisabled: { opacity: 0.5 },
  buttonText: { fontSize: 14, fontWeight: "600" },
  centered: { flex: 1, alignItems: "center", justifyContent: "center", paddingVertical: 32 },
  notice: { borderRadius: 12, padding: 14, marginBottom: 12 },
  noticeTitle: { fontSize: 13, fontWeight: "700" },
  noticeBody: { marginTop: 4, fontSize: 12 },
});
