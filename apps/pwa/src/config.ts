/**
 * Runtime configuration, driven by Vite env vars (see .env.example).
 * All API traffic goes through the APISIX gateway prefixes (SPEC §3.6).
 */
export const config = {
  /** Base URL prepended to every API path. Empty string = same origin (dev proxy / nginx). */
  apiBase: import.meta.env.VITE_API_BASE ?? "",
  keycloak: {
    url: import.meta.env.VITE_KEYCLOAK_URL ?? "http://localhost:8088",
    realm: import.meta.env.VITE_KEYCLOAK_REALM ?? "h2fleet",
    clientId: import.meta.env.VITE_KEYCLOAK_CLIENT_ID ?? "h2fleet-pwa",
  },
  mapStyleUrl:
    import.meta.env.VITE_MAP_STYLE_URL ?? "https://demotiles.maplibre.org/style.json",
  togglePollMs: Number(import.meta.env.VITE_TOGGLE_POLL_MS ?? 30_000),
} as const;

/** APISIX route prefixes (SPEC §3.6). */
export const API_PREFIX = {
  toggles: "/api/toggles",
  fleet: "/api/fleet",
  infra: "/api/infra",
  citizen: "/api/citizen",
  commerce: "/api/commerce",
  ml: "/api/ml",
  optimize: "/api/optimize",
  twin: "/api/twin",
  /** Admin & onboarding backend (admin-api:8085). */
  admin: "/api/admin",
  /** ML inference platform (ml-platform:8095). */
  mlplatform: "/api/mlplatform",
} as const;
