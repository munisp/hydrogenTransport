# toggle-client (Python)

H2Fleet feature-toggle SDK. Identical contract to the Go/TS SDKs (SPEC.md §3.2).

```python
from toggle_client import ToggleClient

toggles = ToggleClient("http://toggle-service:8080")  # TOGGLE_URL env var
if toggles.is_enabled("telematics"):
    ...
```

## Semantics
- `is_enabled(module) -> bool` against `GET /v1/toggles/{module}`.
- **5s local cache** per module (`cache_ttl` configurable).
- **Fail-closed** (`fail_open=False`): any network/parse/non-2xx error ⇒ `False`.
- `ToggleClient` is thread-safe and synchronous; `AsyncToggleClient` provides
  the same contract for asyncio services.
- `info(module)` returns `ToggleInfo(module, enabled, domain)` or `None`.
- `invalidate(module=None)` clears the cache.

## Install / test
```bash
pip install -e .          # from this directory
pip install -e '.[dev]' && pytest
```

Env var convention across services: `TOGGLE_URL=http://toggle-service:8080`.
