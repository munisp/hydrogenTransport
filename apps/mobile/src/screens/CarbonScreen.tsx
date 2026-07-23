import React from "react";
import { FlatList, StyleSheet, Text, View } from "react-native";
import { useQuery } from "@tanstack/react-query";
import { api } from "../api/client";
import { colors } from "../theme";
import { Card, ErrorNotice, Loading, Screen, ScreenTitle } from "../components/ui";

/** Citizen: carbon stats — CO2 avoided and credits issued per period. */
export default function CarbonScreen() {
  const credits = useQuery({ queryKey: ["carbon", "credits"], queryFn: api.listCarbonCredits });

  const data = credits.data ?? [];
  const totalCo2 = data.reduce((s, c) => s + c.kg_co2_avoided, 0);
  const totalCredits = data.reduce((s, c) => s + c.credits, 0);
  const maxCredits = Math.max(1, ...data.map((c) => c.credits));

  return (
    <Screen>
      <ScreenTitle title="Carbon Impact" subtitle="CO2 avoided by riding hydrogen" />

      <View style={styles.statsRow}>
        <Card style={styles.statCard}>
          <Text style={styles.statLabel}>CO2 avoided</Text>
          <Text style={styles.statValue}>{Math.round(totalCo2).toLocaleString()} kg</Text>
        </Card>
        <Card style={styles.statCard}>
          <Text style={styles.statLabel}>Credits issued</Text>
          <Text style={styles.statValue}>{totalCredits.toLocaleString()}</Text>
        </Card>
      </View>

      {credits.isLoading ? (
        <Loading />
      ) : credits.isError ? (
        <ErrorNotice error={credits.error} />
      ) : data.length === 0 ? (
        <Text style={styles.empty}>No credits issued yet.</Text>
      ) : (
        <FlatList
          data={[...data].sort((a, b) => b.period.localeCompare(a.period))}
          keyExtractor={(c) => c.id}
          renderItem={({ item }) => (
            <Card>
              <View style={styles.rowBetween}>
                <Text style={styles.period}>{item.period}</Text>
                <Text style={styles.credits}>{item.credits.toLocaleString()} credits</Text>
              </View>
              <View style={styles.barTrack}>
                <View
                  style={[styles.barFill, { width: `${Math.max(4, (item.credits / maxCredits) * 100)}%` }]}
                />
              </View>
              <Text style={styles.meta}>{Math.round(item.kg_co2_avoided).toLocaleString()} kg CO2 avoided</Text>
            </Card>
          )}
        />
      )}
    </Screen>
  );
}

const styles = StyleSheet.create({
  statsRow: { flexDirection: "row", gap: 12 },
  statCard: { flex: 1 },
  statLabel: { fontSize: 11, fontWeight: "600", color: colors.textMuted, textTransform: "uppercase" },
  statValue: { marginTop: 6, fontSize: 20, fontWeight: "700", color: colors.text, fontVariant: ["tabular-nums"] },
  rowBetween: { flexDirection: "row", justifyContent: "space-between", alignItems: "center" },
  period: { fontSize: 14, fontWeight: "700", color: colors.text },
  credits: { fontSize: 13, fontWeight: "600", color: colors.accent, fontVariant: ["tabular-nums"] },
  barTrack: { marginTop: 8, height: 6, borderRadius: 3, backgroundColor: colors.sunken, overflow: "hidden" },
  barFill: { height: 6, borderRadius: 3, backgroundColor: colors.teal },
  meta: { marginTop: 6, fontSize: 11, color: colors.textMuted },
  empty: { textAlign: "center", color: colors.textFaint, marginTop: 32, fontSize: 13 },
});
