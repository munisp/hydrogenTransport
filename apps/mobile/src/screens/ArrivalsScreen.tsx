import React, { useState } from "react";
import { FlatList, StyleSheet, Text, TouchableOpacity, View } from "react-native";
import { useQuery } from "@tanstack/react-query";
import { api } from "../api/client";
import { config } from "../config";
import { colors } from "../theme";
import { Card, ErrorNotice, Screen, ScreenTitle, Skeleton, formatEta } from "../components/ui";

/** Citizen: live arrivals board for a selected stop. */
export default function ArrivalsScreen() {
  const stops = useQuery({ queryKey: ["stops"], queryFn: api.listStops });
  const [stopId, setStopId] = useState<string | null>(null);
  const effectiveStop = stopId ?? stops.data?.[0]?.stop_id ?? null;

  const arrivals = useQuery({
    queryKey: ["arrivals", effectiveStop],
    queryFn: () => api.listArrivals(effectiveStop as string),
    enabled: effectiveStop !== null,
    refetchInterval: config.arrivalsPollMs,
  });

  return (
    <Screen>
      <ScreenTitle title="Arrivals" subtitle="Live hydrogen bus departures" />

      {stops.isLoading ? (
        <View>
          <View style={styles.skeletonChipRow}>
            {[0, 1, 2, 3].map((i) => (
              <Skeleton key={i} height={34} width={96} style={{ borderRadius: 999, marginRight: 8 }} />
            ))}
          </View>
          <ArrivalSkeletons />
        </View>
      ) : stops.isError ? (
        <ErrorNotice error={stops.error} />
      ) : (
        <>
          <FlatList
            horizontal
            showsHorizontalScrollIndicator={false}
            data={stops.data ?? []}
            keyExtractor={(s) => s.stop_id}
            style={styles.stopList}
            renderItem={({ item }) => (
              <TouchableOpacity
                style={[styles.stopChip, item.stop_id === effectiveStop && styles.stopChipActive]}
                onPress={() => setStopId(item.stop_id)}
              >
                <Text
                  style={[styles.stopChipText, item.stop_id === effectiveStop && styles.stopChipTextActive]}
                >
                  {item.stop_name}
                </Text>
              </TouchableOpacity>
            )}
          />

          {arrivals.isLoading ? (
            <ArrivalSkeletons />
          ) : arrivals.isError ? (
            <ErrorNotice error={arrivals.error} />
          ) : (
            <FlatList
              data={arrivals.data ?? []}
              keyExtractor={(a, i) => `${a.route_id}-${a.scheduled_at}-${i}`}
              refreshing={arrivals.isRefetching}
              onRefresh={() => arrivals.refetch()}
              ListEmptyComponent={
                <Text style={styles.empty}>No upcoming departures at this stop.</Text>
              }
              renderItem={({ item }) => (
                <Card style={styles.arrivalCard}>
                  <View style={styles.routeBadge}>
                    <Text style={styles.routeBadgeText}>{item.route_short_name}</Text>
                  </View>
                  <View style={styles.arrivalBody}>
                    <Text style={styles.headsign} numberOfLines={1}>
                      {item.headsign}
                    </Text>
                    <Text style={styles.arrivalMeta}>
                      {item.in_minutes <= 0 ? "due now" : `in ${item.in_minutes} min`}
                    </Text>
                  </View>
                  <Text style={styles.eta}>{formatEta(item.scheduled_at)}</Text>
                </Card>
              )}
            />
          )}
        </>
      )}
    </Screen>
  );
}

/** Skeleton rows shown while arrivals load (replaces the bare spinner). */
function ArrivalSkeletons() {
  return (
    <View>
      {[0, 1, 2].map((i) => (
        <Card key={i} style={{ flexDirection: "row", alignItems: "center", gap: 12 }}>
          <Skeleton height={32} width={48} />
          <View style={{ flex: 1, gap: 6 }}>
            <Skeleton height={13} width="70%" />
            <Skeleton height={10} width="40%" />
          </View>
          <Skeleton height={15} width={52} />
        </Card>
      ))}
    </View>
  );
}

const styles = StyleSheet.create({
  stopList: { flexGrow: 0, marginBottom: 12 },
  skeletonChipRow: { flexDirection: "row", marginBottom: 12 },
  stopChip: {
    borderRadius: 999,
    borderWidth: 1,
    borderColor: colors.border,
    backgroundColor: colors.card,
    paddingHorizontal: 14,
    paddingVertical: 8,
    marginRight: 8,
  },
  stopChipActive: { backgroundColor: colors.accentSoft, borderColor: colors.accent },
  stopChipText: { fontSize: 13, color: colors.textMuted },
  stopChipTextActive: { color: colors.accent, fontWeight: "600" },
  arrivalCard: { flexDirection: "row", alignItems: "center", gap: 12 },
  routeBadge: {
    backgroundColor: colors.tealSoft,
    borderRadius: 8,
    paddingHorizontal: 10,
    paddingVertical: 6,
    minWidth: 48,
    alignItems: "center",
  },
  routeBadgeText: { color: colors.teal, fontWeight: "700", fontSize: 13 },
  arrivalBody: { flex: 1 },
  headsign: { fontSize: 14, fontWeight: "600", color: colors.text },
  arrivalMeta: { marginTop: 2, fontSize: 11, color: colors.textMuted },
  eta: { fontSize: 15, fontWeight: "700", color: colors.text, fontVariant: ["tabular-nums"] },
  empty: { textAlign: "center", color: colors.textFaint, marginTop: 32, fontSize: 13 },
});
