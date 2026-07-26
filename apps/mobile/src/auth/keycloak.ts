import { setAccessToken } from "../api/client";
import { config } from "../config";

/**
 * Keycloak OIDC integration for the Expo app (SPEC §3.5: RS256 JWT). Unlike
 * the PWA (keycloak-js browser redirect), React Native has no hosted login
 * page in this stack, so the app authenticates directly against the realm
 * token endpoint and then keeps the access token fresh with the refresh
 * grant — same realm/client model as the PWA, real tokens only (nothing is
 * fabricated locally). The token is pushed into the API client via
 * setAccessToken so every authed endpoint sends `Authorization: Bearer`.
 */

export interface AuthIdentity {
  authenticated: boolean;
  username: string;
  roles: string[];
}

interface TokenSet {
  accessToken: string;
  refreshToken: string | undefined;
  /** epoch ms at which the access token expires. */
  expiresAt: number;
}

const ANONYMOUS: AuthIdentity = { authenticated: false, username: "", roles: [] };

let identity: AuthIdentity = ANONYMOUS;
let tokens: TokenSet | null = null;
let refreshTimer: ReturnType<typeof setInterval> | null = null;

/** Renew when less than this much validity remains (mirrors the PWA's 30s). */
const REFRESH_LEEWAY_MS = 30_000;
const REFRESH_POLL_MS = 20_000;

function tokenEndpoint(): string {
  return `${config.keycloak.url}/realms/${config.keycloak.realm}/protocol/openid-connect/token`;
}

/** Decode a JWT payload without a dependency (Hermes has no atob guarantee). */
function decodeJwtPayload(token: string): Record<string, unknown> | null {
  const part = token.split(".")[1];
  if (!part) return null;
  const b64 = part.replace(/-/g, "+").replace(/_/g, "/");
  const alphabet =
    "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/";
  let bits = "";
  for (const ch of b64) {
    if (ch === "=") break;
    const v = alphabet.indexOf(ch);
    if (v < 0) return null;
    bits += v.toString(2).padStart(6, "0");
  }
  let bytes = "";
  for (let i = 0; i + 8 <= bits.length; i += 8) {
    bytes += String.fromCharCode(parseInt(bits.slice(i, i + 8), 2));
  }
  try {
    return JSON.parse(decodeURIComponent(escape(bytes))) as Record<string, unknown>;
  } catch {
    try {
      return JSON.parse(bytes) as Record<string, unknown>;
    } catch {
      return null;
    }
  }
}

function identityFromPayload(payload: Record<string, unknown> | null): AuthIdentity {
  if (!payload) return { authenticated: true, username: "unknown", roles: [] };
  const realmRoles =
    (payload.realm_access as { roles?: string[] } | undefined)?.roles ?? [];
  const clientRoles =
    (payload.resource_access as Record<string, { roles?: string[] }> | undefined)?.[
      config.keycloak.clientId
    ]?.roles ?? [];
  return {
    authenticated: true,
    username:
      (payload.preferred_username as string | undefined) ??
      (payload.email as string | undefined) ??
      (payload.sub as string | undefined) ??
      "unknown",
    roles: [...new Set([...realmRoles, ...clientRoles])],
  };
}

async function requestToken(body: Record<string, string>): Promise<TokenSet> {
  let res: Response;
  try {
    res = await fetch(tokenEndpoint(), {
      method: "POST",
      headers: { "Content-Type": "application/x-www-form-urlencoded" },
      body: new URLSearchParams({ client_id: config.keycloak.clientId, ...body }).toString(),
    });
  } catch (err) {
    throw new Error(
      `Keycloak unreachable: ${err instanceof Error ? err.message : "network error"}`,
    );
  }
  if (!res.ok) {
    const detail = await res.text().catch(() => "");
    let msg = `sign-in failed (${res.status})`;
    try {
      const parsed = JSON.parse(detail) as { error_description?: string; error?: string };
      msg = parsed.error_description ?? parsed.error ?? msg;
    } catch {
      /* keep default message */
    }
    throw new Error(msg);
  }
  const data = (await res.json()) as {
    access_token?: string;
    refresh_token?: string;
    expires_in?: number;
  };
  if (!data.access_token) throw new Error("Keycloak returned no access token");
  return {
    accessToken: data.access_token,
    refreshToken: data.refresh_token,
    expiresAt: Date.now() + (data.expires_in ?? 300) * 1000,
  };
}

function applyTokens(set: TokenSet): void {
  tokens = set;
  setAccessToken(set.accessToken);
  identity = identityFromPayload(decodeJwtPayload(set.accessToken));
}

/** Proactive refresh, mirroring the PWA's updateToken(30) interval. */
function startRefreshLoop(): void {
  stopRefreshLoop();
  refreshTimer = setInterval(() => {
    void refreshIfNeeded();
  }, REFRESH_POLL_MS);
}

function stopRefreshLoop(): void {
  if (refreshTimer) clearInterval(refreshTimer);
  refreshTimer = null;
}

/** Refresh the access token when it is inside the renewal leeway. */
export async function refreshIfNeeded(): Promise<void> {
  if (!tokens || !tokens.refreshToken) return;
  if (Date.now() < tokens.expiresAt - REFRESH_LEEWAY_MS) return;
  try {
    applyTokens(
      await requestToken({
        grant_type: "refresh_token",
        refresh_token: tokens.refreshToken,
      }),
    );
  } catch {
    // The refresh grant rejected the session (expired/revoked): drop back to
    // anonymous rather than letting every authed call 401 forever.
    clearSession();
  }
}

/**
 * Sign in with realm credentials (direct access grant against the Keycloak
 * token endpoint — the mobile stack's equivalent of the PWA's keycloak-js
 * login). On success the access token is wired into the API client and a
 * refresh loop keeps it valid.
 */
export async function login(username: string, password: string): Promise<AuthIdentity> {
  const set = await requestToken({
    grant_type: "password",
    username: username.trim(),
    password,
  });
  applyTokens(set);
  startRefreshLoop();
  return identity;
}

export function logout(): void {
  clearSession();
}

function clearSession(): void {
  stopRefreshLoop();
  tokens = null;
  identity = ANONYMOUS;
  setAccessToken(undefined);
}

export function getIdentity(): AuthIdentity {
  return identity;
}

export function hasRole(role: string): boolean {
  return identity.roles.includes(role);
}
