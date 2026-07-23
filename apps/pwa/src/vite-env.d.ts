/// <reference types="vite/client" />

interface ImportMetaEnv {
  readonly VITE_API_BASE?: string;
  readonly VITE_KEYCLOAK_URL?: string;
  readonly VITE_KEYCLOAK_REALM?: string;
  readonly VITE_KEYCLOAK_CLIENT_ID?: string;
  readonly VITE_MAP_STYLE_URL?: string;
  readonly VITE_TOGGLE_POLL_MS?: string;
}

interface ImportMeta {
  readonly env: ImportMetaEnv;
}
