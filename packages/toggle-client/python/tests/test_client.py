"""Contract tests for the toggle client (5s cache, fail-closed)."""

import time

import httpx
import pytest

from toggle_client import ToggleClient


def make_client(handler, **kwargs):
    transport = httpx.MockTransport(handler)
    http = httpx.Client(base_url="http://toggle:8080", transport=transport)
    return ToggleClient("http://toggle:8080", client=http, **kwargs)


def test_enabled_true():
    def handler(request: httpx.Request) -> httpx.Response:
        assert request.url.path == "/v1/toggles/telematics"
        return httpx.Response(200, json={"module": "telematics", "enabled": True, "domain": "fleet"})

    c = make_client(handler)
    assert c.is_enabled("telematics") is True


def test_fail_closed_on_500_and_timeout():
    c = make_client(lambda r: httpx.Response(500))
    assert c.is_enabled("telematics") is False

    def boom(request):
        raise httpx.ConnectError("down")

    c2 = make_client(boom)
    assert c2.is_enabled("telematics") is False


def test_fail_closed_on_malformed_payload():
    c = make_client(lambda r: httpx.Response(200, json={"unexpected": "shape"}))
    assert c.is_enabled("telematics") is False


def test_cache_ttl():
    calls = {"n": 0}

    def handler(request):
        calls["n"] += 1
        return httpx.Response(200, json={"module": "m", "enabled": True, "domain": "fleet"})

    c = make_client(handler, cache_ttl=5.0)
    assert c.is_enabled("m") is True
    assert c.is_enabled("m") is True
    assert calls["n"] == 1  # second call served from cache

    # Force expiry by rewinding the cache timestamp.
    entry = c._cache["m"]
    entry.fetched_at = time.monotonic() - 6.0
    assert c.is_enabled("m") is True
    assert calls["n"] == 2


def test_info_and_invalidate():
    def handler(request):
        return httpx.Response(200, json={"module": "digital-twin", "enabled": False, "domain": "fleet"})

    c = make_client(handler)
    info = c.info("digital-twin")
    assert info is not None and info.domain == "fleet" and info.enabled is False
    c.is_enabled("digital-twin")
    assert "digital-twin" in c._cache
    c.invalidate("digital-twin")
    assert "digital-twin" not in c._cache


if __name__ == "__main__":
    raise SystemExit(pytest.main([__file__, "-q"]))
