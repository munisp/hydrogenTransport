import Keycloak from "keycloak-js";
import { config } from "../config";

/**
 * Keycloak OIDC integration (SPEC §3.5: RS256 JWT, role `platform-admin` for
 * toggle writes). When the IdP is unreachable in local development the app
 * falls back to a mock platform-admin so screens stay usable without the
 * middleware stack running.
 */

export interface AuthIdentity {
  authenticated: boolean;
  username: string;
  roles: string[];
  /** True when running against the dev fallback instead of a real Keycloak. */
  isDevFallback: boolean;
}

const DEV_IDENTITY: AuthIdentity = {
  authenticated: true,
  username: "dev-admin",
  roles: ["platform-admin", "gov-viewer", "dispatcher", "driver"],
  isDevFallback: true,
};

let keycloak: Keycloak | null = null;
let identity: AuthIdentity = {
  authenticated: false,
  username: "",
  roles: [],
  isDevFallback: false,
};
let refreshTimer: ReturnType<typeof setInterval> | null = null;

function identityFromKeycloak(kc: Keycloak): AuthIdentity {
  const realmRoles = (kc.tokenParsed?.realm_access?.roles as string[] | undefined) ?? [];
  const clientRoles =
    (kc.tokenParsed?.resource_access?.[config.keycloak.clientId]?.roles as
      | string[]
      | undefined) ?? [];
  return {
    authenticated: true,
    username:
      (kc.tokenParsed?.preferred_username as string | undefined) ??
      (kc.tokenParsed?.sub as string | undefined) ??
      "unknown",
    roles: [...new Set([...realmRoles, ...clientRoles])],
    isDevFallback: false,
  };
}

/**
 * Initialise authentication. Resolves with the identity; on Keycloak failure in
 * dev mode resolves with the mock admin identity. In production builds the
 * error propagates so the shell can render a fatal error instead of an
 * unauthenticated app.
 */
export async function initAuth(): Promise<AuthIdentity> {
  try {
    keycloak = new Keycloak({
      url: config.keycloak.url,
      realm: config.keycloak.realm,
      clientId: config.keycloak.clientId,
    });
    await keycloak.init({
      onLoad: "login-required",
      pkceMethod: "S256",
      checkLoginIframe: false,
      silentCheckSsoRedirectUri: `${window.location.origin}/silent-check-sso.html`,
    });
    identity = identityFromKeycloak(keycloak);

    // Proactive token refresh: renew when <30s of validity remain.
    refreshTimer = setInterval(() => {
      keycloak
        ?.updateToken(30)
        .then((refreshed) => {
          if (refreshed && keycloak) identity = identityFromKeycloak(keycloak);
        })
        .catch(() => {
          keycloak?.login();
        });
    }, 20_000);

    return identity;
  } catch (err) {
    keycloak = null;
    if (import.meta.env.DEV) {
      console.warn(
        "[auth] Keycloak unreachable — falling back to mock platform-admin user (dev only).",
        err,
      );
      identity = DEV_IDENTITY;
      return identity;
    }
    throw err instanceof Error ? err : new Error("Authentication initialisation failed");
  }
}

export function getIdentity(): AuthIdentity {
  return identity;
}

export function getAccessToken(): string | undefined {
  return keycloak?.token ?? undefined;
}

export function hasRole(role: string): boolean {
  return identity.roles.includes(role);
}

export function login(): void {
  if (keycloak) void keycloak.login();
}

export function logout(): void {
  if (refreshTimer) clearInterval(refreshTimer);
  if (keycloak) {
    void keycloak.logout({ redirectUri: window.location.origin });
  } else {
    window.location.reload();
  }
}
