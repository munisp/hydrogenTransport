# h2fleet-auth — shared Python JWT auth

Keycloak OIDC JWT (RS256) FastAPI dependency shared by the H2Fleet Python
services (SPEC.md §3.5). Mirrors the Go middleware semantics
(`services/go/*/internal/auth/jwt.go`):

- verifies RS256 tokens against the realm JWKS at `KEYCLOAK_ISSUER`
  (`<issuer>/protocol/openid-connect/certs`), cached for 5 minutes;
  unknown `kid` triggers a refresh; a stale cached key is served if the
  refresh fails (transient JWKS outage);
- accepts the comma-separated `KEYCLOAK_ISSUER_ALT` issuers in addition to
  `KEYCLOAK_ISSUER` (default `http://localhost:8088/realms/h2fleet`, the
  browser-facing alias); trailing slashes are tolerated;
- requires `exp`, rejects other algorithms;
- fail closed: 503 on guarded routes when `KEYCLOAK_ISSUER` is unset,
  401 for missing/invalid tokens.

## Usage

```python
from fastapi import Depends
from h2fleet_auth import KeycloakJwtVerifier

jwt_verifier = KeycloakJwtVerifier.from_env()

@app.post("/v1/thing", dependencies=[Depends(jwt_verifier.require_auth)])
async def thing(...): ...

# on shutdown: await jwt_verifier.aclose()
```

Health endpoints (`/healthz`) stay public — attach the dependency only to
mutating routes.

## Tests

```bash
pip install -e . && pip install pytest
python -m pytest tests -q
```
