# opensearch-bootstrap (Python, one-shot job)

Provisions the OpenSearch backend of the **open-data-portal** module (SPEC §3.8):

1. waits for the cluster (`/_cluster/health`, bounded by `BOOTSTRAP_TIMEOUT_S`),
2. creates the index `h2fleet-open` with explicit mappings
   (`title`/`description`/`content` text fields — the fields citizen-api's
   `/v1/opendata/search` multi_match query targets),
3. upserts the open-data dataset catalog documents served by citizen-api's
   `/v1/opendata/datasets` (mirrors `datasetCatalog` in
   `services/go/citizen-api/internal/handlers/opendata.go`).

Fully idempotent — safe to re-run on every deploy.

## Configuration

| env | default | notes |
|---|---|---|
| `OPENSEARCH_URL` | `http://localhost:9200` | cluster base URL |
| `OPENSEARCH_INDEX` | `h2fleet-open` | must match citizen-api `OPENSEARCH_INDEX` |
| `OPENSEARCH_USER` / `OPENSEARCH_PASSWORD` | unset | optional basic auth |
| `BOOTSTRAP_TIMEOUT_S` | `120` | max wait for the cluster |

## Run

```bash
pip install -r requirements.txt
OPENSEARCH_URL=http://localhost:9200 python bootstrap.py

# or (build context = repository root)
docker build -t h2fleet/opensearch-bootstrap -f services/python/opensearch-bootstrap/Dockerfile .
docker run --rm --network host -e OPENSEARCH_URL=http://localhost:9200 h2fleet/opensearch-bootstrap
```

## docker-compose

The `opensearch-bootstrap` one-shot service is already wired in
`infra/docker-compose.yml` (profiles `apps`/`all`, repo-root build context,
`restart: "no"`, waits for a healthy `opensearch`).
