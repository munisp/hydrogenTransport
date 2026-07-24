"""Shared Keycloak OIDC JWT (RS256) authentication for H2Fleet Python services.

Mirrors the Go middleware semantics (services/go/*/internal/auth/jwt.go):
RS256-only verification against the realm JWKS at KEYCLOAK_ISSUER, accepting
the additional comma-separated KEYCLOAK_ISSUER_ALT issuers, with a 5-minute
JWKS cache and fail-closed behaviour (503 when KEYCLOAK_ISSUER is unset).
"""

from .jwt import DEFAULT_ALT_ISSUER, KeycloakJwtVerifier

__all__ = ["KeycloakJwtVerifier", "DEFAULT_ALT_ISSUER"]
