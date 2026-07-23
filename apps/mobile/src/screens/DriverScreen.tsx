import React, { useState } from "react";
import { FlatList, Modal, Pressable, StyleSheet, Text, TextInput, View } from "react-native";
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

/** Driver tab: assigned jobs from dispatch + one-tap incident report. */
export default function DriverScreen() {
  const queryClient = useQueryClient();
  const [reportOpen, setReportOpen] = useState(false);

  const jobs = useQuery({
    queryKey: ["driver", "jobs"],
    queryFn: () => api.listDispatchJobs(),
    refetchInterval: 30_000,
  });

  const accept = useMutation({
    mutationFn: api.acceptDispatchJob,
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ["driver", "jobs"] }),
  });

  return (
    <Screen>
      <ScreenTitle title="Driver" subtitle="Assigned jobs & incident reporting" />

      <Button
        label="Report an incident"
        variant="danger"
        onPress={() => setReportOpen(true)}
      />
      <View style={{ height: 14 }} />

      {jobs.isLoading ? (
        <Loading />
      ) : jobs.isError ? (
        <ErrorNotice error={jobs.error} />
      ) : (
        <FlatList
          data={jobs.data ?? []}
          keyExtractor={(j) => j.id}
          refreshing={jobs.isRefetching}
          onRefresh={() => jobs.refetch()}
          ListEmptyComponent={<Text style={styles.empty}>No jobs assigned right now.</Text>}
          renderItem={({ item }) => (
            <Card>
              <View style={styles.rowBetween}>
                <View style={{ flex: 1 }}>
                  <Text style={styles.jobTitle}>
                    Route {item.route}
                    {item.vehicle_id ? ` · Bus ${item.vehicle_id.slice(0, 8)}` : ""}
                  </Text>
                  <Text style={styles.jobMeta}>
                    Starts {formatDateTime(item.starts_at ?? "")}
                  </Text>
                </View>
                <StatusBadge status={item.status} />
              </View>
              {item.status === "assigned" ? (
                <View style={{ marginTop: 10 }}>
                  <Button
                    label="Accept job"
                    variant="secondary"
                    busy={accept.isPending && accept.variables === item.id}
                    onPress={() => accept.mutate(item.id)}
                  />
                </View>
              ) : null}
            </Card>
          )}
        />
      )}

      <IncidentReportModal visible={reportOpen} onClose={() => setReportOpen(false)} />
    </Screen>
  );
}

const INCIDENT_TYPES = ["breakdown", "leak", "collision", "station_fault", "security"] as const;
const SEVERITIES = ["low", "medium", "high", "critical"] as const;

function IncidentReportModal({ visible, onClose }: { visible: boolean; onClose: () => void }) {
  const [type, setType] = useState<(typeof INCIDENT_TYPES)[number]>("breakdown");
  const [severity, setSeverity] = useState<(typeof SEVERITIES)[number]>("medium");
  const [description, setDescription] = useState("");

  const report = useMutation({
    mutationFn: api.reportIncident,
    onSuccess: () => {
      setDescription("");
      setTimeout(onClose, 1200);
    },
  });

  return (
    <Modal visible={visible} animationType="slide" transparent onRequestClose={onClose}>
      <View style={styles.backdrop}>
        <View style={styles.sheet}>
          <Text style={styles.sheetTitle}>Report incident</Text>

          <Text style={styles.inputLabel}>Type</Text>
          <View style={styles.chipRow}>
            {INCIDENT_TYPES.map((t) => (
              <Pressable
                key={t}
                style={[styles.chip, type === t && styles.chipActive]}
                onPress={() => setType(t)}
              >
                <Text style={[styles.chipText, type === t && styles.chipTextActive]}>
                  {t.replace(/_/g, " ")}
                </Text>
              </Pressable>
            ))}
          </View>

          <Text style={styles.inputLabel}>Severity</Text>
          <View style={styles.chipRow}>
            {SEVERITIES.map((s) => (
              <Pressable
                key={s}
                style={[styles.chip, severity === s && styles.chipActive]}
                onPress={() => setSeverity(s)}
              >
                <Text style={[styles.chipText, severity === s && styles.chipTextActive]}>{s}</Text>
              </Pressable>
            ))}
          </View>

          <Text style={styles.inputLabel}>Description</Text>
          <TextInput
            style={[styles.input, styles.textArea]}
            value={description}
            onChangeText={setDescription}
            placeholder="What happened?"
            placeholderTextColor={colors.textFaint}
            multiline
          />

          {report.isError ? <ErrorNotice error={report.error} /> : null}
          {report.isSuccess ? (
            <Notice tone="green" title="Reported" body="Dispatch has been notified." />
          ) : null}

          <View style={styles.sheetActions}>
            <View style={{ flex: 1 }}>
              <Button label="Cancel" variant="secondary" onPress={onClose} />
            </View>
            <View style={{ flex: 1 }}>
              <Button
                label="Send"
                variant="danger"
                busy={report.isPending}
                disabled={description.trim().length === 0}
                onPress={() =>
                  report.mutate({ type, severity, description: description.trim() })
                }
              />
            </View>
          </View>
        </View>
      </View>
    </Modal>
  );
}

const styles = StyleSheet.create({
  rowBetween: { flexDirection: "row", justifyContent: "space-between", alignItems: "center", gap: 10 },
  jobTitle: { fontSize: 14, fontWeight: "700", color: colors.text },
  jobMeta: { marginTop: 2, fontSize: 11, color: colors.textMuted },
  empty: { textAlign: "center", color: colors.textFaint, marginTop: 32, fontSize: 13 },
  backdrop: { flex: 1, backgroundColor: "rgba(28,25,23,0.4)", justifyContent: "flex-end" },
  sheet: {
    backgroundColor: colors.bg,
    borderTopLeftRadius: 20,
    borderTopRightRadius: 20,
    padding: 20,
    paddingBottom: 32,
  },
  sheetTitle: { fontSize: 17, fontWeight: "700", color: colors.text, marginBottom: 12 },
  inputLabel: { fontSize: 11, fontWeight: "600", color: colors.textMuted, marginTop: 10, marginBottom: 6 },
  chipRow: { flexDirection: "row", flexWrap: "wrap", gap: 8 },
  chip: {
    borderRadius: 999,
    borderWidth: 1,
    borderColor: colors.border,
    backgroundColor: colors.card,
    paddingHorizontal: 12,
    paddingVertical: 7,
  },
  chipActive: { backgroundColor: colors.accentSoft, borderColor: colors.accent },
  chipText: { fontSize: 12, color: colors.textMuted },
  chipTextActive: { color: colors.accent, fontWeight: "600" },
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
  textArea: { minHeight: 80, textAlignVertical: "top" },
  sheetActions: { flexDirection: "row", gap: 10, marginTop: 16 },
});
