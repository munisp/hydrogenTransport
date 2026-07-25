#!/usr/bin/env python3
"""Push APISIX routes/consumers/global rules from the git-managed standalone
file (infra/apisix/apisix.yaml) into the etcd-backed control plane via the
Admin API. Idempotent: everything is a PUT keyed by stable ids.

Env:
  APISIX_ADMIN_URL   default http://apisix:9180
  APISIX_ADMIN_KEY   required (matches config.yaml.prod admin_key)
  PWA_ORIGIN         rendered into ${{PWA_ORIGIN:=...}} placeholders
  OPENAPPSEC_MODE    "detect" or "prevent" — rendered into the openappsec
                     global rule (prod sets "prevent")

Runs in the apisix-etcd-sync one-shot (python:3.12-alpine + pyyaml).
"""
import json
import os
import re
import sys
import time
import urllib.error
import urllib.request

import yaml

ADMIN = os.environ.get("APISIX_ADMIN_URL", "http://apisix:9180").rstrip("/")
KEY = os.environ["APISIX_ADMIN_KEY"]
SRC = "/src/apisix.yaml"


def load():
    with open(SRC, encoding="utf-8") as fh:
        text = fh.read()
    # Render the two env placeholders the standalone file uses; APISIX only
    # expands ${{VAR}} in config.yaml, not in etcd-pushed objects, so the sync
    # job must substitute before pushing.
    pwa_origin = os.environ.get("PWA_ORIGIN", "https://app.h2fleet.example.com")
    text = text.replace("${{PWA_ORIGIN:=http://localhost:3000}}", pwa_origin)
    doc = yaml.safe_load(text)
    if os.environ.get("OPENAPPSEC_MODE"):
        for rule in doc.get("global_rules", []):
            oas = rule.get("plugins", {}).get("openappsec")
            if isinstance(oas, dict):
                oas["mode"] = os.environ["OPENAPPSEC_MODE"]
    return doc


def put(path, obj, retries=12):
    body = json.dumps(obj).encode()
    req = urllib.request.Request(
        ADMIN + path,
        data=body,
        method="PUT",
        headers={"X-API-KEY": KEY, "Content-Type": "application/json"},
    )
    for attempt in range(retries):
        try:
            with urllib.request.urlopen(req, timeout=10) as resp:
                if resp.status not in (200, 201):
                    raise RuntimeError(f"PUT {path}: HTTP {resp.status}: {resp.read()!r}")
                return
        except (urllib.error.URLError, TimeoutError, RuntimeError) as exc:
            if attempt == retries - 1:
                raise
            wait = min(2 ** attempt, 15)
            print(f"PUT {path} failed ({exc}); retry {attempt + 1}/{retries} in {wait}s", flush=True)
            time.sleep(wait)


def main():
    doc = load()
    pushed = 0
    for idx, route in enumerate(doc.get("routes", []), start=1):
        # Standalone routes rarely carry explicit ids; derive a URL-safe one
        # from the uri so PUTs are stable/idempotent across sync runs.
        rid = route.get("id")
        if not rid:
            raw = route.get("uri") or route.get("uris", ["route"])[0]
            rid = re.sub(r"[^A-Za-z0-9]+", "-", raw).strip("-") or f"route-{idx}"
            route["id"] = rid
        put(f"/apisix/admin/routes/{rid}", route)
        pushed += 1
    for svc in doc.get("services", []):
        put(f"/apisix/admin/services/{svc['id']}", svc)
        pushed += 1
    for up in doc.get("upstreams", []):
        put(f"/apisix/admin/upstreams/{up['id']}", up)
        pushed += 1
    for consumer in doc.get("consumers", []):
        put(f"/apisix/admin/consumers/{consumer['username']}", consumer)
        pushed += 1
    for rule in doc.get("global_rules", []):
        put(f"/apisix/admin/global_rules/{rule['id']}", rule)
        pushed += 1
    for ssl in doc.get("ssls", []) or []:
        put(f"/apisix/admin/ssls/{ssl['id']}", ssl)
        pushed += 1
    print(f"synced {pushed} objects from {SRC} into etcd via {ADMIN}", flush=True)


if __name__ == "__main__":
    try:
        main()
    except Exception as exc:  # noqa: BLE001 — one-shot job: fail loudly
        print(f"FATAL: {exc}", file=sys.stderr, flush=True)
        sys.exit(1)
