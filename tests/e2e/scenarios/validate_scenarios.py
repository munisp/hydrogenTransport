#!/usr/bin/env python3
"""H2Fleet scenario static validator (make validate-scenarios).

Validates tests/e2e/scenarios/manifest.yaml against the services' committed
openapi.yaml files so CI can check scenario -> API coverage WITHOUT a live
stack:

  1. every referenced service has a readable openapi.yaml;
  2. every step (method, path) exists as an operation in that openapi
     (path templates normalized: /v1/x/{id} == /v1/x/{anything});
  3. a step `body` satisfies the operation's requestBody schema:
     all `required` properties present; unknown properties flagged
     ($ref + allOf resolved locally);
  4. drift guard: the step's path appears in the scenario's s*.sh script
     (literal match after stripping the gateway prefix), so scripts and
     manifest cannot silently diverge;
  5. every s*.sh in the directory is covered by a manifest entry.

Exits 0 when everything validates; prints [FAIL] lines and exits 1 otherwise.
"""
from __future__ import annotations

import re
import sys
from pathlib import Path

import yaml

HERE = Path(__file__).resolve().parent
REPO = HERE.parents[2]
METHODS = {"get", "post", "put", "patch", "delete", "head", "options"}

failures: list[str] = []
checks = 0


def fail(msg: str) -> None:
    failures.append(msg)
    print(f"[FAIL] {msg}")


def ok(msg: str) -> None:
    global checks
    checks += 1
    print(f"[ ok ] {msg}")


def norm_path(p: str) -> str:
    return re.sub(r"\{[^}]+\}", "{}", p)


def resolve(spec: dict, node):
    """Resolve local $ref chains and flatten allOf."""
    seen = set()
    while isinstance(node, dict) and "$ref" in node:
        ref = node["$ref"]
        if not ref.startswith("#/") or ref in seen:
            return {}
        seen.add(ref)
        for part in ref[2:].split("/"):
            node = spec.get(part, {})
    if isinstance(node, dict) and "allOf" in node:
        merged: dict = {"type": "object", "properties": {}, "required": []}
        for sub in node["allOf"]:
            sub = resolve(spec, sub)
            merged["properties"].update(sub.get("properties", {}) or {})
            merged["required"] += sub.get("required", []) or []
        return merged
    return node if isinstance(node, dict) else {}


def request_schema(spec: dict, op: dict) -> dict:
    content = (op.get("requestBody", {}) or {}).get("content", {}) or {}
    media = content.get("application/json", {}) or {}
    return resolve(spec, media.get("schema", {}) or {})


def main() -> int:
    manifest = yaml.safe_load((HERE / "manifest.yaml").read_text())
    services = manifest["services"]

    specs: dict[str, dict] = {}
    for name, meta in services.items():
        oapi = REPO / meta["openapi"]
        if not oapi.is_file():
            fail(f"service {name}: openapi not found at {meta['openapi']}")
            continue
        specs[name] = yaml.safe_load(oapi.read_text())
        ok(f"service {name}: loaded {meta['openapi']}")

    scripts_on_disk = {p.name for p in HERE.glob("s[0-9][0-9]_*.sh")}
    scripts_in_manifest = set()

    for sid, sc in sorted(manifest["scenarios"].items()):
        script = sc["script"]
        scripts_in_manifest.add(script)
        if script not in scripts_on_disk:
            fail(f"{sid}: script {script} missing on disk")
            script_text = ""
        else:
            script_text = (HERE / script).read_text()
            ok(f"{sid} ({sc['name']}): script {script} present")

        for i, step in enumerate(sc["steps"], 1):
            svc, method, path = step["service"], step["method"].lower(), step["path"]
            label = f"{sid} step {i}: {method.upper()} {svc}{path}"
            spec = specs.get(svc)
            if spec is None:
                fail(f"{label}: unknown service")
                continue

            op = None
            for p, item in (spec.get("paths") or {}).items():
                if norm_path(p) == norm_path(path) and method in item:
                    op = item[method]
                    break
            if op is None:
                fail(f"{label}: operation missing in {svc} openapi")
                continue

            body = step.get("body")
            if body is not None:
                schema = request_schema(spec, op)
                required = set(schema.get("required", []) or [])
                props = set((schema.get("properties") or {}).keys())
                missing = required - set(body.keys())
                if missing:
                    fail(f"{label}: body misses required {sorted(missing)}")
                    continue
                unknown = set(body.keys()) - props if props else set()
                if unknown:
                    fail(f"{label}: body has unknown props {sorted(unknown)} (schema: {sorted(props)})")
                    continue
            ok(label)

            # Drift guard: the script must reference the path (or its static
            # prefix for templated paths), optionally behind a gateway prefix.
            static = path.split("{")[0].rstrip("/") or path
            if static not in script_text:
                fail(f"{label}: path {static} not found in {script}")

    uncovered = scripts_on_disk - scripts_in_manifest
    if uncovered:
        fail(f"scenario scripts not covered by manifest: {sorted(uncovered)}")

    print("-" * 64)
    if failures:
        print(f"validate-scenarios: {len(failures)} FAILURE(S) across {checks + len(failures)} checks")
        return 1
    print(f"validate-scenarios: OK — {checks} checks passed "
          f"({len(manifest['scenarios'])} scenarios, "
          f"{sum(len(s['steps']) for s in manifest['scenarios'].values())} steps)")
    return 0


if __name__ == "__main__":
    sys.exit(main())
