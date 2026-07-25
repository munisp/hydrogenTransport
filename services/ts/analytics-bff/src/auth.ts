// Keycloak OIDC JWT (RS256) middleware for analytics-bff via jose — mirrors
// packages/go-auth semantics so TS services cannot drift from the Go ones:
//
//   * issuers: KEYCLOAK_ISSUER (also used for JWKS fetching) plus the
//     optional comma-separated KEYCLOAK_ISSUER_ALT (default
//     http://localhost:8088/realms/h2fleet, the browser-facing issuer).
//   * audience: validated only when KEYCLOAK_AUDIENCE is set.
//   * fail-closed: when KEYCLOAK_ISSUER is unset every guarded route 503s.
//   * realm roles read from realm_access.roles, same as go-auth.
import { createRemoteJWKSet, jwtVerify, type JWTPayload } from "jose";
import type { Context, Next } from "hono";

export interface AuthConfig {
  issuer: string; // KEYCLOAK_ISSUER ("" = auth not configured -> 503)
  issuerAlt: string; // comma-separated extra accepted issuers
  audience: string; // KEYCLOAK_AUDIENCE ("" = skip aud validation)
}

export interface AuthEnv {
  Variables: {
    claims: JWTPayload;
  };
}

export function loadAuthConfig(env: NodeJS.ProcessEnv = process.env): AuthConfig {
  return {
    issuer: (env.KEYCLOAK_ISSUER ?? "").replace(/\/$/, ""),
    issuerAlt:
      env.KEYCLOAK_ISSUER_ALT ?? "http://localhost:8088/realms/h2fleet",
    audience: env.KEYCLOAK_AUDIENCE ?? "",
  };
}

export function realmRoles(claims: JWTPayload | undefined): string[] {
  const ra = claims?.realm_access as { roles?: unknown } | undefined;
  return Array.isArray(ra?.roles) ? (ra.roles as string[]) : [];
}

// jwtAuth validates Bearer tokens against the realm JWKS and stores claims
// on c.var.claims.
export function jwtAuth(cfg: AuthConfig) {
  const issuers = [cfg.issuer, ...cfg.issuerAlt.split(",").map((s) => s.trim().replace(/\/$/, ""))].filter(Boolean);
  const jwks = cfg.issuer
    ? createRemoteJWKSet(new URL(`${cfg.issuer}/protocol/openid-connect/certs`))
    : null;

  return async (c: Context<AuthEnv>, next: Next) => {
    if (!jwks) {
      return c.json({ error: "auth not configured" }, 503);
    }
    const header = c.req.header("authorization") ?? "";
    const token = header.startsWith("Bearer ") ? header.slice(7) : "";
    if (!token) {
      return c.json({ error: "missing bearer token" }, 401);
    }
    try {
      const { payload } = await jwtVerify(token, jwks, {
        issuer: issuers,
        ...(cfg.audience ? { audience: cfg.audience } : {}),
      });
      c.set("claims", payload);
      await next();
    } catch {
      return c.json({ error: "invalid or expired token" }, 401);
    }
  };
}

// requireAnyRole allows the request when the principal carries at least one
// of the given realm roles (mirrors httpx.RequireAnyRole in admin-api).
export function requireAnyRole(...roles: string[]) {
  return async (c: Context<AuthEnv>, next: Next) => {
    const have = realmRoles(c.get("claims"));
    if (!roles.some((r) => have.includes(r))) {
      return c.json({ error: `missing required realm role (any of): ${roles.join(", ")}` }, 403);
    }
    await next();
  };
}
