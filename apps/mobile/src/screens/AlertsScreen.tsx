import React from "react";
import { FlatList, StyleSheet, Text } from "react-native";
import { useQuery } from "@tanstack/react-query";
import { api } from "../api/client";
import { colors } from "../theme";
import { Card, ErrorNotice, Loading, Screen, ScreenTitle } from "../components/ui";

/** Citizen: service alerts feed. */
export default function AlertsScreen() {
  const alerts = useQuery({
    queryKey: ["alerts"],
    queryFn: api.listAlerts,
    refetchInterval: 60_000,
  });

  return (
    <Screen>
      <ScreenTitle title="Service Alerts" subtitle="Disruptions and planned works" />
      {alerts.isLoading ? (
        <Loading />
      ) : alerts.isError ? (
        <ErrorNotice error={alerts.error} />
      ) : (
        <FlatList
          data={alerts.data ?? []}
          keyExtractor={(a) => a.id}
          refreshing={alerts.isRefetching}
          onRefresh={() => alerts.refetch()}
          ListEmptyComponent={
            <Text style={styles.empty}>No active alerts — services running normally.</Text>
          }
          renderItem={({ item }) => (
            <Card>
              <Text style={styles.title}>{item.header}</Text>
              <Text style={styles.body}>{item.description}</Text>
              <Text style={styles.meta}>
                {item.route_ids && item.route_ids.length > 0
                  ? `Routes: ${item.route_ids.join(", ")}`
                  : "Network-wide"}
              </Text>
            </Card>
          )}
        />
      )}
    </Screen>
  );
}

const styles = StyleSheet.create({
  title: { fontSize: 14, fontWeight: "700", color: colors.text },
  body: { marginTop: 4, fontSize: 13, color: colors.textMuted, lineHeight: 18 },
  meta: { marginTop: 6, fontSize: 11, color: colors.textFaint },
  empty: { textAlign: "center", color: colors.textFaint, marginTop: 32, fontSize: 13 },
});
