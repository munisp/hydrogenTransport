import Constants from "expo-constants";

/**
 * Runtime config comes from app.json `expo.extra` — the single place deployments
 * override the APISIX gateway base and Keycloak settings (SPEC §3.5/§3.6).
 */
interface ExtraConfig {
  apiBase?: string;
  keycloak?: { url?: string; realm?: string; clientId?: string };
  arrivalsPollMs?: number;
  togglesPollMs?: number;
}

const extra = (Constants.expoConfig?.extra ?? {}) as ExtraConfig;

export const config = {
  /** APISIX gateway base URL; all prefixes (SPEC §3.6) hang off this. */
  apiBase: extra.apiBase ?? "http://localhost:9080",
  keycloak: {
    url: extra.keycloak?.url ?? "http://localhost:8088",
    realm: extra.keycloak?.realm ?? "h2fleet",
    clientId: extra.keycloak?.clientId ?? "h2fleet-mobile",
  },
  arrivalsPollMs: extra.arrivalsPollMs ?? 20_000,
  togglesPollMs: extra.togglesPollMs ?? 30_000,
} as const;
