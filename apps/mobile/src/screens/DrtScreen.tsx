import React, { useState } from "react";
import { StyleSheet, Text, TextInput, View } from "react-native";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api } from "../api/client";
import { colors } from "../theme";
import {
  Button,
  Card,
  ErrorNotice,
  Loading,
  Notice,
  Screen,
  ScreenTitle,
  StatusBadge,
  formatDateTime,
} from "../components/ui";

/** Citizen: demand-responsive shuttle request (drt.requested). */
export default function DrtScreen() {
  const queryClient = useQueryClient();
  const [pickupLat, setPickupLat] = useState("");
  const [pickupLon, setPickupLon] = useState("");
  const [dropLat, setDropLat] = useState("");
  const [dropLon, setDropLon] = useState("");

  const requests = useQuery({
    queryKey: ["drt", "requests"],
    queryFn: api.listDrtRequests,
    refetchInterval: 20_000,
  });

  const create = useMutation({
    mutationFn: api.createDrtRequest,
    onSuccess: () => {
      setPickupLat("");
      setPickupLon("");
      setDropLat("");
      setDropLon("");
      void queryClient.invalidateQueries({ queryKey: ["drt"] });
    },
  });

  const coords = [pickupLat, pickupLon, dropLat, dropLon].map(Number);
  const valid = coords.every((n) => Number.isFinite(n) && n !== 0);

  function submit() {
    if (!valid) return;
    create.mutate({
      pickup: { lat: coords[0] as number, lon: coords[1] as number },
      dropoff: { lat: coords[2] as number, lon: coords[3] as number },
    });
  }

  return (
    <Screen>
      <ScreenTitle title="On-Demand Shuttle" subtitle="Request a DRT pickup in the service zone" />

      <Card>
        <Text style={styles.sectionLabel}>Pickup</Text>
        <View style={styles.row}>
          <CoordInput label="Latitude" value={pickupLat} onChange={setPickupLat} placeholder="50.0755" />
          <CoordInput label="Longitude" value={pickupLon} onChange={setPickupLon} placeholder="14.4378" />
        </View>
        <Text style={[styles.sectionLabel, { marginTop: 10 }]}>Dropoff</Text>
        <View style={styles.row}>
          <CoordInput label="Latitude" value={dropLat} onChange={setDropLat} placeholder="50.0875" />
          <CoordInput label="Longitude" value={dropLon} onChange={setDropLon} placeholder="14.4210" />
        </View>
        <View style={{ marginTop: 14 }}>
          <Button label="Request pickup" onPress={submit} busy={create.isPending} disabled={!valid} />
        </View>
        {create.isError ? <ErrorNotice error={create.error} /> : null}
        {create.isSuccess ? (
          <View style={{ marginTop: 10 }}>
            <Notice tone="green" title="Request received" body="Dispatch is matching a shuttle to your pickup." />
          </View>
        ) : null}
      </Card>

      <Text style={styles.listHeader}>My requests</Text>
      {requests.isLoading ? (
        <Loading />
      ) : requests.isError ? (
        <ErrorNotice error={requests.error} />
      ) : (requests.data ?? []).length === 0 ? (
        <Text style={styles.empty}>No requests yet.</Text>
      ) : (
        (requests.data ?? []).slice(0, 8).map((r) => (
          <Card key={r.id} style={styles.requestCard}>
            <View style={{ flex: 1 }}>
              <Text style={styles.requestTitle}>
                {formatCoord(r.pickup_lat)}, {formatCoord(r.pickup_lon)} → {formatCoord(r.dropoff_lat)},{" "}
                {formatCoord(r.dropoff_lon)}
              </Text>
              <Text style={styles.requestMeta}>{formatDateTime(r.requested_at)}</Text>
            </View>
            <StatusBadge status={r.status} />
          </Card>
        ))
      )}
    </Screen>
  );
}

/** Scalar lat/lon fields are omitempty on the backend; render a dash when absent. */
function formatCoord(value: number | null | undefined): string {
  return typeof value === "number" ? value.toFixed(3) : "—";
}

function CoordInput({
  label,
  value,
  onChange,
  placeholder,
}: {
  label: string;
  value: string;
  onChange: (v: string) => void;
  placeholder: string;
}) {
  return (
    <View style={{ flex: 1 }}>
      <Text style={styles.inputLabel}>{label}</Text>
      <TextInput
        style={styles.input}
        value={value}
        onChangeText={onChange}
        placeholder={placeholder}
        placeholderTextColor={colors.textFaint}
        keyboardType="numbers-and-punctuation"
        autoCapitalize="none"
      />
    </View>
  );
}

const styles = StyleSheet.create({
  sectionLabel: { fontSize: 12, fontWeight: "700", color: colors.textMuted, marginBottom: 6 },
  row: { flexDirection: "row", gap: 10 },
  inputLabel: { fontSize: 11, color: colors.textMuted, marginBottom: 4 },
  input: {
    borderWidth: 1,
    borderColor: colors.border,
    borderRadius: 10,
    backgroundColor: colors.card,
    paddingHorizontal: 12,
    paddingVertical: 10,
    fontSize: 14,
    color: colors.text,
  },
  listHeader: { fontSize: 14, fontWeight: "700", color: colors.text, marginVertical: 10 },
  requestCard: { flexDirection: "row", alignItems: "center", gap: 10 },
  requestTitle: { fontSize: 13, fontWeight: "600", color: colors.text },
  requestMeta: { marginTop: 2, fontSize: 11, color: colors.textMuted },
  empty: { textAlign: "center", color: colors.textFaint, marginTop: 16, fontSize: 13 },
});
