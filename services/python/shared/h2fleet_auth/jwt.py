"""Keycloak OIDC JWT (RS256) FastAPI dependency (SPEC.md §3.5).

Semantics mirror services/go/*/internal/auth/jwt.go:

* Public keys are fetched from the in-network realm JWKS endpoint derived from
  ``KEYCLOAK_ISSUER`` (``<issuer>/protocol/openid-connect/certs``) and cached
  for 5 minutes; an unknown ``kid`` triggers a refresh, and a stale cached key
  is served if the refresh fails (transient JWKS outage).
* The ``iss`` claim is validated against the accepted issuer set:
  ``KEYCLOAK_ISSUER`` plus the comma-separated ``KEYCLOAK_ISSUER_ALT``
  (default ``http://localhost:8088/realms/h2fleet``, the browser-facing alias).
* Only RS256 is accepted and ``exp`` is required.
* Fail closed: when ``KEYCLOAK_ISSUER`` is empty every guarded route answers
  503; missing/invalid tokens get 401.

Usage in a FastAPI service::

    from h2fleet_auth import KeycloakJwtVerifier

    jwt_verifier = KeycloakJwtVerifier.from_env()

    @app.post("/v1/thing", dependencies=[Depends(jwt_verifier.require_auth)])
    async def thing(...): ...

``require_auth`` returns the validated claims dict to handlers that inject it
as a parameter instead of a router dependency.
"""

from __future__ import annotations

import asyncio
import json
import logging
import os
import time

import httpx
import jwt
from fastapi import HTTPException, Request
from jwt.algorithms import RSAAlgorithm

log = logging.getLogger("h2fleet-auth")

#: Public (browser-facing) Keycloak issuer accepted in addition to the
#: in-network KEYCLOAK_ISSUER when KEYCLOAK_ISSUER_ALT is unset.
DEFAULT_ALT_ISSUER = "http://localhost:8088/realms/h2fleet"

JWKS_CACHE_TTL_S = 300.0  # 5-minute JWKS cache (mirrors Go middleware)
_REFRESH_MIN_INTERVAL_S = 10.0  # single-flight-ish refresh throttle
_HTTP_TIMEOUT_S = 5.0


class KeycloakJwtVerifier:
    """Verifies RS256 JWTs issued by a Keycloak realm against its JWKS."""

    def __init__(self, issuer: str, alt_issuers: str | None = None) -> None:
        issuer = issuer.rstrip("/")
        issuers: list[str] = [issuer] if issuer else []
        alt = DEFAULT_ALT_ISSUER if alt_issuers is None else alt_issuers
        for a in alt.split(","):
            a = a.strip().rstrip("/")
            if a and a not in issuers:
                issuers.append(a)

        self._issuers: tuple[str, ...] = tuple(issuers)
        # JWKS is fetched from the in-network issuer only (never the aliases).
        self._jwks_url = (
            f"{issuer}/protocol/openid-connect/certs" if issuer else ""
        )
        self._keys: dict[str, RSAAlgorithm] = {}
        self._fetched_at = 0.0
        self._lock = asyncio.Lock()
        self._http = httpx.AsyncClient(timeout=_HTTP_TIMEOUT_S)

        if not issuer:
            log.warning(
                "KEYCLOAK_ISSUER not set; JWT-protected routes will reject requests"
            )
        else:
            log.info("jwt verifier configured; accepted_issuers=%s", list(issuers))

    @classmethod
    def from_env(cls) -> "KeycloakJwtVerifier":
        """Build from KEYCLOAK_ISSUER / KEYCLOAK_ISSUER_ALT (SPEC §3.5 env)."""
        return cls(
            issuer=os.environ.get("KEYCLOAK_ISSUER", ""),
            alt_issuers=os.environ.get("KEYCLOAK_ISSUER_ALT") or None,
        )

    async def aclose(self) -> None:
        await self._http.aclose()

    # ------------------------------------------------------------------ API
    async def require_auth(self, request: Request) -> dict:
        """FastAPI dependency: 401/503 on failure, claims dict on success."""
        if not self._jwks_url:
            raise HTTPException(
                status_code=503,
                detail="authentication not configured (KEYCLOAK_ISSUER unset)",
            )
        token = _bearer_token(request)
        if not token:
            raise HTTPException(status_code=401, detail="missing bearer token")
        try:
            return await self.verify(token)
        except Exception as exc:
            log.debug("jwt verification failed: %s", exc)
            raise HTTPException(status_code=401, detail="invalid token") from exc

    async def verify(self, token: str) -> dict:
        """Verify a raw JWT and return its claims (raises on any failure)."""
        header = jwt.get_unverified_header(token)
        kid = header.get("kid", "")
        key = await self._public_key(kid)
        claims = jwt.decode(
            token,
            key=key,
            algorithms=["RS256"],
            options={
                "require": ["exp", "iss"],
                "verify_aud": False,
                "verify_exp": True,
                # iss is checked manually below (trailing-slash tolerant,
                # mirrors the Go middleware's acceptsIssuer).
                "verify_iss": False,
            },
        )
        iss = str(claims.get("iss", "")).rstrip("/")
        if iss not in self._issuers:
            raise jwt.InvalidIssuerError(f"unexpected issuer {claims.get('iss')!r}")
        return claims

    # ----------------------------------------------------------------- JWKS
    async def _public_key(self, kid: str):
        key = self._keys.get(kid)
        stale = (time.monotonic() - self._fetched_at) > JWKS_CACHE_TTL_S
        if key is not None and not stale:
            return key
        try:
            await self._refresh_jwks()
        except Exception as exc:
            if key is not None:
                # Serve the stale key rather than failing on a transient
                # JWKS outage (mirrors the Go middleware).
                log.warning("jwks refresh failed, serving stale key: %s", exc)
                return key
            raise
        key = self._keys.get(kid)
        if key is None:
            raise jwt.InvalidTokenError(f"unknown kid {kid!r}")
        return key

    async def _refresh_jwks(self) -> None:
        async with self._lock:
            # Another coroutine may have refreshed while we waited.
            if (time.monotonic() - self._fetched_at) < _REFRESH_MIN_INTERVAL_S:
                return
            resp = await self._http.get(self._jwks_url)
            resp.raise_for_status()
            doc = resp.json()
            keys: dict[str, RSAAlgorithm] = {}
            for jwk in doc.get("keys", []):
                if jwk.get("kty") != "RSA":
                    continue
                try:
                    keys[jwk.get("kid", "")] = RSAAlgorithm.from_jwk(
                        json.dumps(jwk)
                    )
                except Exception as exc:
                    log.warning(
                        "skipping unparsable jwks key kid=%s: %s",
                        jwk.get("kid"),
                        exc,
                    )
            if not keys:
                raise ValueError("jwks document contained no usable RSA keys")
            self._keys = keys
            self._fetched_at = time.monotonic()


def _bearer_token(request: Request) -> str:
    header = request.headers.get("authorization", "")
    if not header.startswith("Bearer "):
        return ""
    return header[len("Bearer ") :].strip()
