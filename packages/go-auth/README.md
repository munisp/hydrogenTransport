# go-auth — shared auth module for H2Fleet Go services

Single source of truth for authentication/authorization middleware, replacing
the per-service `internal/auth` copies (SPEC §3.5). Services consume it via a
`replace` directive (same pattern as the toggle-client SDK):

```go
require github.com/munisp/hydrogenTransport/packages/go-auth v0.0.0
replace github.com/munisp/hydrogenTransport/packages/go-auth => ../../../packages/go-auth
```

## JWT middleware (`jwt.go`)

- `New(issuer, log)` — Keycloak RS256 JWT middleware. JWKS is fetched from the
  in-network `KEYCLOAK_ISSUER`; tokens may carry `KEYCLOAK_ISSUER` or any
  comma-separated `KEYCLOAK_ISSUER_ALT` issuer (default
  `http://localhost:8088/realms/h2fleet`).
- **Audience**: when `KEYCLOAK_AUDIENCE` is set, tokens must contain it in
  `aud`. When unset, `aud` is not validated (dev convenience only).
- `RequireAuth` / `RequireRole(role)` middleware; `Subject(ctx)`,
  `HasRole(ctx, role)`, `HasAnyRole(ctx, roles...)` helpers.

## Permify checks (`permify.go`)

Minimal hand-written gRPC client for `permify.v1.PermissionService/Check`
against `PERMIFY_GRPC` (e.g. `permify:3476`), tenant `PERMIFY_TENANT`
(default `t1`).

**Fallback contract (fail-closed where it matters):** when `PERMIFY_GRPC` is
set, checks are enforced — DENIED → 403, check/transport error → 502 (never
silently allowed). When `PERMIFY_GRPC` is **unset**, `NewPermify` returns nil
and the `Require` middleware passes through after logging a one-time warning,
so routes rely on the Keycloak realm-role check alone (role-only fallback for
local dev; do not unset `PERMIFY_GRPC` in production).
