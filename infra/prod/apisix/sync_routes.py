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
  KEYCLOAK_SERVICES_CLIENT_SECRET
                     required — rendered into the openid-connect client_secret
                     placeholders; APISIX only expands ${{VAR}} in config.yaml,
                     never in etcd-pushed route objects.
  H2_PARTNER_API_KEY optional — rendered into the data-partner key-auth
                     consumer. When unset the placeholder consumer is NOT
                     pushed (fail-closed: no partner access) instead of
                     publishing the REPLACE_ME placeholder as a working key.

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
    # Render the env placeholders the standalone file uses; APISIX only
    # expands ${{VAR}} in config.yaml, not in etcd-pushed objects, so the sync
    # job must substitute before pushing.
    pwa_origin = os.environ.get("PWA_ORIGIN", "https://app.h2fleet.example.com")
    text = text.replace("${{PWA_ORIGIN:=http://localhost:3000}}", pwa_origin)
    # openid-connect client_secret on every route: required — pushing the
    # REPLACE_ME placeholder would leave the gateway with a broken (and
    # publicly known) secret value.
    kc_secret = os.environ.get("KEYCLOAK_SERVICES_CLIENT_SECRET")
    if not kc_secret:
        raise RuntimeError(
            "KEYCLOAK_SERVICES_CLIENT_SECRET is required: refusing to push routes "
            "with the REPLACE_ME openid-connect client_secret placeholder"
        )
    text = text.replace(
        "${{KEYCLOAK_SERVICES_CLIENT_SECRET:=REPLACE_ME_VIA_KEYCLOAK_SERVICES_CLIENT_SECRET}}",
        kc_secret,
    )
    doc = yaml.safe_load(text)
    # data-partner key-auth consumer: replace the placeholder key from env, or
    # drop the consumer entirely (fail-closed) when no key was provisioned —
    # never publish REPLACE_ME_PROVISION_PER_PARTNER as a working credential.
    partner_key = os.environ.get("H2_PARTNER_API_KEY")
    consumers = doc.get("consumers", []) or []
    kept = []
    for consumer in consumers:
        key = (consumer.get("plugins", {}).get("key-auth") or {}).get("key")
        if key == "REPLACE_ME_PROVISION_PER_PARTNER":
            if not partner_key:
                print(
                    f"H2_PARTNER_API_KEY unset: dropping consumer {consumer.get('username')!r} "
                    "(no partner access until a key is provisioned)",
                    flush=True,
                )
                continue
            consumer["plugins"]["key-auth"]["key"] = partner_key
        kept.append(consumer)
    doc["consumers"] = kept
    if os.environ.get("OPENAPPSEC_MODE"):
        for rule in doc.get("global_rules", []):
            oas = rule.get("plugins", {}).get("openappsec")
            if isinstance(oas, dict):
                oas["mode"] = os.environ["OPENAPPSEC_MODE"]
    # Safety net: nothing pushed to etcd may still contain a placeholder.
    rendered = json.dumps(doc)
    if "REPLACE_ME" in rendered or "${{" in rendered:
        raise RuntimeError("unsubstituted placeholders remain after rendering; refusing to push")
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
