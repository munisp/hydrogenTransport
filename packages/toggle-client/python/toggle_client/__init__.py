"""H2Fleet feature-toggle client SDK (Python).

Contract (identical to the Go/TS SDKs, SPEC.md §3.2):
    is_enabled(module) -> bool
      * 5s local cache per module
      * fail-open = false: on any error the module is treated as DISABLED
"""

from .client import AsyncToggleClient, ToggleClient, ToggleInfo

__all__ = ["ToggleClient", "AsyncToggleClient", "ToggleInfo"]
__version__ = "0.1.0"
