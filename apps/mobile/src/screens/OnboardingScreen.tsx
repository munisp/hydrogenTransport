import React, { useState } from "react";
import {
  KeyboardAvoidingView,
  Platform,
  Pressable,
  ScrollView,
  StyleSheet,
  Text,
  TextInput,
  View,
} from "react-native";
import { useMutation } from "@tanstack/react-query";
import { Ionicons } from "@expo/vector-icons";
import { api } from "../api/client";
import { colors } from "../theme";
import { Button, Card, ErrorNotice, Screen, ScreenTitle } from "../components/ui";

/**
 * First-run onboarding: persona select → tailored form → instant citizen
 * account or an approval-pending receipt (admin-api public intake).
 */

type PersonaId =
  | "citizen"
  | "driver"
  | "operator"
  | "station-staff"
  | "advertiser"
  | "data-partner"
  | "gov-viewer";

interface PersonaDef {
  id: PersonaId;
  label: string;
  tagline: string;
  icon: React.ComponentProps<typeof Ionicons>["name"];
  instant: boolean;
  orgLabel?: string;
}

const PERSONAS: PersonaDef[] = [
  { id: "citizen", label: "Citizen", tagline: "Arrivals, DRT shuttles & carbon credits. Instant account.", icon: "person-outline", instant: true },
  { id: "driver", label: "Driver", tagline: "Jobs, dispatch & incident reporting. Needs approval.", icon: "bus-outline", instant: false },
  { id: "operator", label: "Operator", tagline: "Fleet & infrastructure consoles. Needs approval.", icon: "business-outline", instant: false },
  { id: "station-staff", label: "Station staff", tagline: "Refueling stations & safety. Needs approval.", icon: "flame-outline", instant: false },
  { id: "advertiser", label: "Advertiser", tagline: "On-bus ad inventory & campaigns. Needs approval.", icon: "megaphone-outline", instant: false },
  { id: "data-partner", label: "Data partner", tagline: "Open data & GTFS feeds. Needs approval.", icon: "server-outline", instant: false },
  { id: "gov-viewer", label: "Gov viewer", tagline: "City KPI dashboard. Needs approval.", icon: "ribbon-outline", instant: false },
];

type Stage =
  | { kind: "select" }
  | { kind: "form"; persona: PersonaDef }
  | { kind: "pending"; persona: PersonaDef; email: string };

const EMAIL_RE = /^[^@\s]+@[^@\s]+\.[^@\s]+$/;

export default function OnboardingScreen({ onDone }: { onDone: () => void }) {
  const [stage, setStage] = useState<Stage>({ kind: "select" });

  return (
    <Screen>
      <KeyboardAvoidingView
        style={{ flex: 1 }}
        behavior={Platform.OS === "ios" ? "padding" : undefined}
      >
        <ScrollView keyboardShouldPersistTaps="handled" showsVerticalScrollIndicator={false}>
          {stage.kind === "select" ? (
            <PersonaSelect
              onSelect={(persona) => setStage({ kind: "form", persona })}
              onSkip={onDone}
            />
          ) : stage.kind === "form" ? (
            <PersonaForm
              persona={stage.persona}
              onBack={() => setStage({ kind: "select" })}
              onSubmitted={(email) => setStage({ kind: "pending", persona: stage.persona, email })}
              onCitizenDone={onDone}
            />
          ) : (
            <PendingReceipt
              persona={stage.persona}
              email={stage.email}
              onDone={onDone}
            />
          )}
        </ScrollView>
      </KeyboardAvoidingView>
    </Screen>
  );
}

function PersonaSelect({
  onSelect,
  onSkip,
}: {
  onSelect: (p: PersonaDef) => void;
  onSkip: () => void;
}) {
  return (
    <View>
      <ScreenTitle title="Join H2Fleet" subtitle="Pick the role that fits you to get started" />
      {PERSONAS.map((p) => (
        <Pressable
          key={p.id}
          onPress={() => onSelect(p)}
          style={({ pressed }) => [styles.personaCard, pressed && styles.personaCardPressed]}
          accessibilityRole="button"
          accessibilityLabel={`${p.label}: ${p.tagline}`}
        >
          <View style={styles.personaIcon}>
            <Ionicons name={p.icon} size={22} color={colors.accent} />
          </View>
          <View style={{ flex: 1 }}>
            <View style={styles.personaTitleRow}>
              <Text style={styles.personaLabel}>{p.label}</Text>
              {p.instant ? (
                <Text style={styles.instantPill}>instant</Text>
              ) : (
                <Text style={styles.approvalPill}>approval</Text>
              )}
            </View>
            <Text style={styles.personaTagline}>{p.tagline}</Text>
          </View>
          <Ionicons name="chevron-forward" size={16} color={colors.textFaint} />
        </Pressable>
      ))}
      <Button label="Browse as guest for now" variant="secondary" onPress={onSkip} />
    </View>
  );
}

function PersonaForm({
  persona,
  onBack,
  onSubmitted,
  onCitizenDone,
}: {
  persona: PersonaDef;
  onBack: () => void;
  onSubmitted: (email: string) => void;
  onCitizenDone: () => void;
}) {
  const [name, setName] = useState("");
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [org, setOrg] = useState("");

  const valid =
    name.trim().length > 0 &&
    EMAIL_RE.test(email.trim()) &&
    (persona.instant ? password.length >= 8 : org.trim().length > 0);

  const submit = useMutation({
    mutationFn: async () => {
      if (persona.instant) {
        return api.onboardCitizen({
          email: email.trim(),
          display_name: name.trim(),
          password,
        });
      }
      return api.submitOnboarding(persona.id, {
        email: email.trim(),
        display_name: name.trim(),
        org: org.trim(),
      });
    },
    onSuccess: () => {
      if (persona.instant) onCitizenDone();
      else onSubmitted(email.trim());
    },
  });

  return (
    <View>
      <Pressable onPress={onBack} accessibilityRole="button" style={styles.backRow}>
        <Ionicons name="chevron-back" size={16} color={colors.textMuted} />
        <Text style={styles.backText}>All roles</Text>
      </Pressable>
      <ScreenTitle
        title={`${persona.label} sign-up`}
        subtitle={persona.instant ? "Your account activates instantly" : "An operator approves your access"}
      />

      <Card>
        <Text style={styles.inputLabel}>Full name</Text>
        <TextInput
          style={styles.input}
          value={name}
          onChangeText={setName}
          placeholder="Ada Lovelace"
          placeholderTextColor={colors.textFaint}
          autoComplete="name"
        />
        <Text style={styles.inputLabel}>Email</Text>
        <TextInput
          style={styles.input}
          value={email}
          onChangeText={setEmail}
          placeholder="you@example.org"
          placeholderTextColor={colors.textFaint}
          keyboardType="email-address"
          autoCapitalize="none"
          autoComplete="email"
        />
        {persona.instant ? (
          <>
            <Text style={styles.inputLabel}>Password</Text>
            <TextInput
              style={styles.input}
              value={password}
              onChangeText={setPassword}
              placeholder="At least 8 characters"
              placeholderTextColor={colors.textFaint}
              secureTextEntry
              autoComplete="new-password"
            />
          </>
        ) : (
          <>
            <Text style={styles.inputLabel}>Organisation</Text>
            <TextInput
              style={styles.input}
              value={org}
              onChangeText={setOrg}
              placeholder="e.g. City Transit Authority"
              placeholderTextColor={colors.textFaint}
              autoComplete="organization"
            />
          </>
        )}
      </Card>

      {submit.isError ? <ErrorNotice error={submit.error} /> : null}

      <Button
        label={persona.instant ? "Create my account" : "Submit for approval"}
        busy={submit.isPending}
        disabled={!valid}
        onPress={() => submit.mutate()}
      />
    </View>
  );
}

function PendingReceipt({
  persona,
  email,
  onDone,
}: {
  persona: PersonaDef;
  email: string;
  onDone: () => void;
}) {
  return (
    <View style={styles.pendingWrap}>
      <View style={styles.pendingIcon}>
        <Ionicons name="time-outline" size={40} color={colors.accent} />
      </View>
      <Text style={styles.pendingTitle}>Approval pending</Text>
      <Text style={styles.pendingBody}>
        Your {persona.label.toLowerCase()} request has been submitted. You will receive
        an email at {email} once the operations team activates your account.
      </Text>
      <Button label="Continue to the app" variant="secondary" onPress={onDone} />
    </View>
  );
}

const styles = StyleSheet.create({
  personaCard: {
    flexDirection: "row",
    alignItems: "center",
    gap: 12,
    backgroundColor: colors.card,
    borderRadius: 14,
    borderWidth: 1,
    borderColor: colors.border,
    padding: 14,
    marginBottom: 10,
  },
  personaCardPressed: { backgroundColor: colors.accentSoft, borderColor: colors.accent },
  personaIcon: {
    backgroundColor: colors.accentSoft,
    borderRadius: 10,
    padding: 8,
  },
  personaTitleRow: { flexDirection: "row", alignItems: "center", gap: 8 },
  personaLabel: { fontSize: 14, fontWeight: "700", color: colors.text },
  instantPill: {
    fontSize: 10,
    fontWeight: "600",
    color: colors.teal,
    backgroundColor: colors.tealSoft,
    borderRadius: 999,
    paddingHorizontal: 6,
    paddingVertical: 1,
    overflow: "hidden",
  },
  approvalPill: {
    fontSize: 10,
    fontWeight: "600",
    color: colors.textMuted,
    backgroundColor: colors.sunken,
    borderRadius: 999,
    paddingHorizontal: 6,
    paddingVertical: 1,
    overflow: "hidden",
  },
  personaTagline: { marginTop: 2, fontSize: 12, color: colors.textMuted },
  backRow: { flexDirection: "row", alignItems: "center", gap: 2, marginBottom: 8 },
  backText: { fontSize: 13, color: colors.textMuted },
  inputLabel: { fontSize: 11, fontWeight: "600", color: colors.textMuted, marginTop: 8, marginBottom: 6 },
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
  pendingWrap: { alignItems: "center", paddingTop: 64, paddingHorizontal: 16 },
  pendingIcon: {
    backgroundColor: colors.accentSoft,
    borderRadius: 999,
    padding: 18,
    marginBottom: 16,
  },
  pendingTitle: { fontSize: 20, fontWeight: "700", color: colors.text },
  pendingBody: {
    marginTop: 8,
    marginBottom: 24,
    textAlign: "center",
    fontSize: 13,
    lineHeight: 20,
    color: colors.textMuted,
  },
});
