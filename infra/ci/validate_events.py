#!/usr/bin/env python3
"""Validate packages/events fixtures against their JSON schemas (SPEC §3.3).

For every packages/events/fixtures/<topic>.json, the schema
packages/events/schemas/<topic>.json must exist and the fixture must
validate. Schemas themselves are sanity-checked as valid Draft 2020-12.

Exit code 0 = all good, 1 = at least one failure (CI-blocking).
"""
from __future__ import annotations

import json
import sys
from pathlib import Path

from jsonschema import Draft202012Validator

ROOT = Path(__file__).resolve().parents[2]
SCHEMAS = ROOT / "packages" / "events" / "schemas"
FIXTURES = ROOT / "packages" / "events" / "fixtures"


def main() -> int:
    failures = 0

    # 1. All schemas parse and are valid Draft 2020-12.
    for schema_path in sorted(SCHEMAS.glob("*.json")):
        try:
            schema = json.loads(schema_path.read_text())
            Draft202012Validator.check_schema(schema)
        except Exception as err:  # noqa: BLE001
            print(f"SCHEMA INVALID {schema_path.name}: {err}")
            failures += 1

    # 2. Every fixture validates against the same-named schema.
    fixtures = sorted(FIXTURES.glob("*.json"))
    if not fixtures:
        print(f"no fixtures found in {FIXTURES} — nothing to validate")
    for fixture_path in fixtures:
        schema_path = SCHEMAS / fixture_path.name
        if not schema_path.exists():
            print(f"FIXTURE ORPHAN {fixture_path.name}: no schema with the same name")
            failures += 1
            continue
        schema = json.loads(schema_path.read_text())
        doc = json.loads(fixture_path.read_text())
        errors = sorted(
            Draft202012Validator(schema).iter_errors(doc), key=lambda e: e.json_path
        )
        if errors:
            failures += 1
            for err in errors:
                print(f"FIXTURE INVALID {fixture_path.name}: {err.json_path}: {err.message}")
        else:
            print(f"ok {fixture_path.name}")

    # 3. Envelope contract spot-check: `type` must equal the fixture filename stem.
    for fixture_path in fixtures:
        doc = json.loads(fixture_path.read_text())
        if doc.get("type") != fixture_path.stem:
            print(
                f"ENVELOPE MISMATCH {fixture_path.name}: "
                f"type={doc.get('type')!r} != {fixture_path.stem!r}"
            )
            failures += 1

    print(f"{'FAILED' if failures else 'PASSED'}: {failures} problem(s)")
    return 1 if failures else 0


if __name__ == "__main__":
    sys.exit(main())
