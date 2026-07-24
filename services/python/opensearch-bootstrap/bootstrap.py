#!/usr/bin/env python3
"""opensearch-bootstrap: create the open-data index and seed the dataset catalog.

One-shot provisioning job (SPEC §3.8 — OpenSearch backs the open-data-portal
module). Creates the index `h2fleet-open` (env OPENSEARCH_INDEX) with explicit
mappings and upserts the dataset catalog documents served by citizen-api's
/v1/opendata endpoints (see services/go/citizen-api/internal/handlers/opendata.go).

Idempotent: existing index is kept (mappings PUT is a no-op when compatible);
catalog documents are upserted by stable _id.

Env:
    OPENSEARCH_URL       default http://localhost:9200
    OPENSEARCH_INDEX     default h2fleet-open
    OPENSEARCH_USER      optional basic-auth user
    OPENSEARCH_PASSWORD  optional basic-auth password
    BOOTSTRAP_TIMEOUT_S  max seconds to wait for the cluster (default 120)
"""

from __future__ import annotations

import json
import logging
import os
import sys
import time
from datetime import datetime, timezone

import requests

logging.basicConfig(
    level=logging.INFO, format="%(asctime)s %(levelname)s %(name)s %(message)s"
)
log = logging.getLogger("opensearch-bootstrap")

OPENSEARCH_URL = os.environ.get("OPENSEARCH_URL", "http://localhost:9200").rstrip("/")
INDEX = os.environ.get("OPENSEARCH_INDEX", "h2fleet-open")
TIMEOUT_S = float(os.environ.get("BOOTSTRAP_TIMEOUT_S", "120"))

AUTH = None
if os.environ.get("OPENSEARCH_USER"):
    AUTH = (os.environ["OPENSEARCH_USER"], os.environ.get("OPENSEARCH_PASSWORD", ""))

INDEX_BODY = {
    "settings": {"number_of_shards": 1, "number_of_replicas": 0},
    "mappings": {
        "dynamic": True,
        "properties": {
            "id": {"type": "keyword"},
            "title": {"type": "text", "fields": {"keyword": {"type": "keyword"}}},
            "description": {"type": "text"},
            "content": {"type": "text"},
            "format": {"type": "keyword"},
            "url": {"type": "keyword"},
            "updated_at": {"type": "date"},
        },
    },
}

# Mirrors datasetCatalog in services/go/citizen-api/internal/handlers/opendata.go;
# `content` carries the extra full-text the /v1/opendata/search multi_match
# (title^2, description, content) can hit.
CATALOG_DOCS = [
    {
        "id": "gtfs-static",
        "title": "GTFS static feed",
        "description": "Stops, routes and timetable of the H2 bus network.",
        "format": "GTFS",
        "url": "/api/citizen/v1/passenger/routes",
        "content": (
            "GTFS static feed: stops.txt, routes.txt, trips.txt, stop_times.txt "
            "and calendar for the hydrogen bus network. Timetable, stop "
            "geometry (WGS84), route shapes and headways."
        ),
    },
    {
        "id": "arrivals",
        "title": "Stop arrivals",
        "description": "Next departures per stop (derived from the static timetable).",
        "format": "JSON",
        "url": "/api/citizen/v1/passenger/arrivals?stop_id=S001",
        "content": (
            "Real-time style stop arrivals and next departures per stop_id, "
            "derived from the static timetable and live fleet telemetry."
        ),
    },
    {
        "id": "carbon-credits",
        "title": "CO2 avoidance ledger",
        "description": "Issued carbon credits per reporting period.",
        "format": "JSON",
        "url": "/api/citizen/v1/carbon/credits",
        "content": (
            "Open carbon ledger: kg of CO2 avoided by the hydrogen fleet versus "
            "the diesel baseline, and issued credits per monthly/weekly "
            "reporting period."
        ),
    },
    {
        "id": "service-alerts",
        "title": "Service alerts",
        "description": "Active service disruptions and notices.",
        "format": "JSON",
        "url": "/api/citizen/v1/passenger/alerts",
        "content": (
            "Active service alerts: disruptions, detours, station maintenance "
            "windows and passenger notices for the H2 bus network."
        ),
    },
]


def wait_for_cluster(session: requests.Session) -> None:
    deadline = time.monotonic() + TIMEOUT_S
    attempt = 0
    while True:
        attempt += 1
        try:
            r = session.get(f"{OPENSEARCH_URL}/_cluster/health", timeout=5)
            if r.ok:
                log.info("cluster reachable: status=%s", r.json().get("status"))
                return
            log.warning("cluster health HTTP %s (attempt %d)", r.status_code, attempt)
        except requests.RequestException as exc:
            log.warning("cluster not ready (attempt %d): %s", attempt, exc)
        if time.monotonic() >= deadline:
            log.error("cluster not reachable within %.0fs", TIMEOUT_S)
            sys.exit(1)
        time.sleep(min(2.0 * attempt, 10.0))


def ensure_index(session: requests.Session) -> None:
    r = session.head(f"{OPENSEARCH_URL}/{INDEX}", timeout=5)
    if r.status_code == 200:
        log.info("index %s already exists; ensuring mappings", INDEX)
        m = session.put(
            f"{OPENSEARCH_URL}/{INDEX}/_mapping",
            json=INDEX_BODY["mappings"],
            timeout=10,
        )
        m.raise_for_status()
        return
    log.info("creating index %s", INDEX)
    r = session.put(f"{OPENSEARCH_URL}/{INDEX}", json=INDEX_BODY, timeout=10)
    if r.status_code == 400 and "resource_already_exists_exception" in r.text:
        return  # raced with another bootstrap run
    r.raise_for_status()


def seed_catalog(session: requests.Session) -> None:
    now = datetime.now(timezone.utc).isoformat()
    lines: list[str] = []
    for doc in CATALOG_DOCS:
        body = {**doc, "updated_at": now}
        lines.append(json.dumps({"index": {"_index": INDEX, "_id": doc["id"]}}))
        lines.append(json.dumps(body))
    payload = "\n".join(lines) + "\n"
    r = session.post(
        f"{OPENSEARCH_URL}/_bulk?refresh=true",
        data=payload,
        headers={"Content-Type": "application/x-ndjson"},
        timeout=15,
    )
    r.raise_for_status()
    result = r.json()
    if result.get("errors"):
        failures = [i for i in result.get("items", []) if "error" in i.get("index", {})]
        log.error("bulk indexing had %d failures: %s", len(failures), failures[:2])
        sys.exit(1)
    log.info("seeded %d catalog documents into %s", len(CATALOG_DOCS), INDEX)


def main() -> None:
    session = requests.Session()
    if AUTH:
        session.auth = AUTH
    wait_for_cluster(session)
    ensure_index(session)
    seed_catalog(session)
    log.info("opensearch bootstrap complete")


if __name__ == "__main__":
    main()
