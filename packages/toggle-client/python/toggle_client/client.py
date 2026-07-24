"""Synchronous + asynchronous feature-toggle client for H2Fleet.

Talks to the toggle-service REST API (SPEC.md §3.2):

    GET /v1/toggles/{module} -> {"module": "<id>", "enabled": true, "domain": "<domain>"}

Semantics:
  * Results are cached locally for ``cache_ttl`` seconds (default 5s).
  * Fail-closed: network errors, non-2xx responses, or malformed payloads all
    resolve to ``False`` (module treated as disabled).
"""

from __future__ import annotations

import threading
import time
from dataclasses import dataclass

import httpx


@dataclass(frozen=True)
class ToggleInfo:
    module: str
    enabled: bool
    domain: str = ""


@dataclass
class _CacheEntry:
    enabled: bool
    fetched_at: float  # time.monotonic()


class ToggleClient:
    """Feature-toggle client with a 5s local cache, fail-closed semantics."""

    def __init__(
        self,
        url: str,
        *,
        cache_ttl: float = 5.0,
        timeout: float = 2.0,
        client: httpx.Client | None = None,
    ) -> None:
        if not url:
            raise ValueError("toggle-service url must not be empty")
        self._base = url.rstrip("/")
        self._cache_ttl = cache_ttl
        self._client = client or httpx.Client(base_url=self._base, timeout=timeout)
        self._owns_client = client is None
        self._cache: dict[str, _CacheEntry] = {}
        self._lock = threading.Lock()

    # -- public API ---------------------------------------------------------
    def is_enabled(self, module: str) -> bool:
        """Return True iff ``module`` is enabled. Never raises; fails closed."""
        try:
            now = time.monotonic()
            with self._lock:
                entry = self._cache.get(module)
                if entry is not None and (now - entry.fetched_at) < self._cache_ttl:
                    return entry.enabled
            enabled = self._fetch(module)
            with self._lock:
                self._cache[module] = _CacheEntry(enabled=enabled, fetched_at=time.monotonic())
            return enabled
        except Exception:
            # Fail-closed: any unexpected error => disabled.
            return False

    def info(self, module: str) -> ToggleInfo | None:
        """Return full toggle info, or None on error (does not use the cache)."""
        try:
            resp = self._client.get(f"/v1/toggles/{module}")
            if resp.status_code != 200:
                return None
            body = resp.json()
            return ToggleInfo(
                module=str(body.get("module", module)),
                enabled=bool(body.get("enabled", False)),
                domain=str(body.get("domain", "")),
            )
        except Exception:
            return None

    def invalidate(self, module: str | None = None) -> None:
        """Drop cached entries (one module, or all when module is None)."""
        with self._lock:
            if module is None:
                self._cache.clear()
            else:
                self._cache.pop(module, None)

    def close(self) -> None:
        if self._owns_client:
            self._client.close()

    def __enter__(self) -> "ToggleClient":
        return self

    def __exit__(self, *exc: object) -> None:
        self.close()

    # -- internals ----------------------------------------------------------
    def _fetch(self, module: str) -> bool:
        resp = self._client.get(f"/v1/toggles/{module}")
        if resp.status_code != 200:
            return False
        body = resp.json()
        return bool(body.get("enabled", False))


class AsyncToggleClient:
    """Async variant with identical semantics (5s cache, fail-closed)."""

    def __init__(
        self,
        url: str,
        *,
        cache_ttl: float = 5.0,
        timeout: float = 2.0,
        client: httpx.AsyncClient | None = None,
    ) -> None:
        if not url:
            raise ValueError("toggle-service url must not be empty")
        self._base = url.rstrip("/")
        self._cache_ttl = cache_ttl
        self._client = client or httpx.AsyncClient(base_url=self._base, timeout=timeout)
        self._owns_client = client is None
        self._cache: dict[str, _CacheEntry] = {}

    async def is_enabled(self, module: str) -> bool:
        try:
            entry = self._cache.get(module)
            if entry is not None and (time.monotonic() - entry.fetched_at) < self._cache_ttl:
                return entry.enabled
            resp = await self._client.get(f"/v1/toggles/{module}")
            enabled = resp.status_code == 200 and bool(resp.json().get("enabled", False))
            self._cache[module] = _CacheEntry(enabled=enabled, fetched_at=time.monotonic())
            return enabled
        except Exception:
            return False

    def invalidate(self, module: str | None = None) -> None:
        if module is None:
            self._cache.clear()
        else:
            self._cache.pop(module, None)

    async def close(self) -> None:
        if self._owns_client:
            await self._client.aclose()
