"""Focused tests for h2fleet_auth.KeycloakJwtVerifier.

Run:  python -m pytest services/python/shared/tests -q
(needs: pytest, PyJWT[crypto], httpx, fastapi)
"""

from __future__ import annotations

import asyncio
import time

import jwt as pyjwt
import pytest
from cryptography.hazmat.primitives import serialization
from cryptography.hazmat.primitives.asymmetric import rsa
from fastapi import HTTPException
from jwt.algorithms import RSAAlgorithm
from starlette.requests import Request

from h2fleet_auth import KeycloakJwtVerifier

ISSUER = "http://keycloak:8080/realms/h2fleet"
ALT_ISSUER = "http://localhost:8088/realms/h2fleet"


def _make_keypair():
    priv = rsa.generate_private_key(public_exponent=65537, key_size=2048)
    priv_pem = priv.private_bytes(
        serialization.Encoding.PEM,
        serialization.PrivateFormat.PKCS8,
        serialization.NoEncryption(),
    )
    pub_jwk = RSAAlgorithm.to_jwk(priv.public_key(), as_dict=True)
    pub_jwk["kid"] = "test-key-1"
    pub_jwk["kty"] = "RSA"
    return priv_pem, pub_jwk


PRIV_PEM, PUB_JWK = _make_keypair()


class _FakeResponse:
    def __init__(self, doc):
        self._doc = doc

    def raise_for_status(self):
        pass

    def json(self):
        return self._doc


class _FakeHTTP:
    """Stands in for httpx.AsyncClient (only .get is used)."""

    def __init__(self, jwks):
        self._jwks = jwks
        self.calls = 0

    async def get(self, url):
        self.calls += 1
        return _FakeResponse(self._jwks)


def _verifier(issuer=ISSUER, alt=None, jwks=None):
    v = KeycloakJwtVerifier(issuer=issuer, alt_issuers=alt)
    v._http = _FakeHTTP(jwks if jwks is not None else {"keys": [PUB_JWK]})
    return v


def _request(token: str | None) -> Request:
    headers = []
    if token is not None:
        headers.append((b"authorization", f"Bearer {token}".encode()))
    return Request(
        {
            "type": "http",
            "method": "POST",
            "path": "/",
            "headers": headers,
            "query_string": b"",
        }
    )


def _token(iss=ISSUER, exp_delta=3600, kid="test-key-1", key=PRIV_PEM):
    return pyjwt.encode(
        {"sub": "user-1", "iss": iss, "exp": int(time.time()) + exp_delta},
        key,
        algorithm="RS256",
        headers={"kid": kid},
    )


def test_valid_token_primary_issuer():
    v = _verifier()
    claims = asyncio.run(v.require_auth(_request(_token())))
    assert claims["sub"] == "user-1"
    assert v._http.calls == 1  # JWKS fetched once


def test_valid_token_alt_issuer():
    v = _verifier()  # default alt = browser-facing issuer
    claims = asyncio.run(v.require_auth(_request(_token(iss=ALT_ISSUER))))
    assert claims["sub"] == "user-1"


def test_jwks_cached_for_five_minutes():
    v = _verifier()
    asyncio.run(v.require_auth(_request(_token())))
    asyncio.run(v.require_auth(_request(_token())))
    assert v._http.calls == 1


def test_unknown_kid_rejected():
    v = _verifier()
    with pytest.raises(HTTPException) as exc:
        asyncio.run(v.require_auth(_request(_token(kid="nope"))))
    assert exc.value.status_code == 401


def test_wrong_issuer_rejected():
    v = _verifier()
    with pytest.raises(HTTPException) as exc:
        asyncio.run(
            v.require_auth(_request(_token(iss="http://evil.example/realms/x")))
        )
    assert exc.value.status_code == 401


def test_expired_token_rejected():
    v = _verifier()
    with pytest.raises(HTTPException) as exc:
        asyncio.run(v.require_auth(_request(_token(exp_delta=-60))))
    assert exc.value.status_code == 401


def test_missing_token_rejected():
    v = _verifier()
    with pytest.raises(HTTPException) as exc:
        asyncio.run(v.require_auth(_request(None)))
    assert exc.value.status_code == 401


def test_unconfigured_issuer_fails_closed_503():
    v = _verifier(issuer="")
    with pytest.raises(HTTPException) as exc:
        asyncio.run(v.require_auth(_request(_token())))
    assert exc.value.status_code == 503


def test_explicit_alt_issuers_comma_separated():
    v = _verifier(alt="https://sso.example.com/realms/h2fleet, https://other.example/x")
    for iss in ("https://sso.example.com/realms/h2fleet", "https://other.example/x"):
        claims = asyncio.run(v.require_auth(_request(_token(iss=iss))))
        assert claims["sub"] == "user-1"


def test_trailing_slash_issuer_accepted():
    v = _verifier()
    claims = asyncio.run(v.require_auth(_request(_token(iss=ISSUER + "/"))))
    assert claims["sub"] == "user-1"
