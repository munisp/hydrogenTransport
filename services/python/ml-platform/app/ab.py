"""Deterministic champion/challenger assignment.

The same subject key (bus id, route id, ...) always lands in the same variant
for a given model and split — assignment is a stable hash, not a coin flip, so
A/B analysis can compare like-for-like and tests are reproducible.
"""

from __future__ import annotations

import hashlib


def assign_variant(model: str, subject_key: str, split: float,
                   challenger_available: bool) -> str:
    """-> "champion" | "challenger". `split` is the challenger fraction."""
    if not challenger_available or split <= 0.0:
        return "champion"
    if split >= 1.0:
        return "challenger"
    digest = hashlib.sha256(f"{model}:{subject_key}".encode()).hexdigest()
    frac = int(digest[:12], 16) / float(0xFFFFFFFFFFFF)
    return "challenger" if frac < split else "champion"
