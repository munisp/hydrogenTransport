# H2Fleet on Kubernetes (kustomize)

Kustomize layout for deploying H2Fleet to Kubernetes. The **base** contains the
namespace, shared `h2fleet-config` ConfigMap, `h2fleet-secrets` Secret, all 11
workload manifests (Deployment + Service each), Ingress, HPAs,
PodDisruptionBudgets and NetworkPolicies. Overlays tailor a deployment to a
domain subset — this is how a *per-deployment profile* (e.g. a city pilot
running only fleet + infra) is expressed in k8s.

```
infra/k8s/
  base/                 namespace, configmap, secret, 11 services, ingress, hpa, pdb, networkpolicy
  overlays/dev/         dev overlay: TOGGLE_DOMAINS=fleet,infra, 1 replica, :dev images, HPAs removed
```

## Apply

```bash
kubectl apply -k infra/k8s/overlays/dev        # dev / city-pilot profile
kubectl apply -k infra/k8s/base                # all domains (TOGGLE_DOMAINS=all)
kubectl kustomize infra/k8s/overlays/dev       # render without applying
```

## Secrets

`base/secret.yaml` carries **dev-only** values (`DATABASE_URL`,
`LEAK_INGEST_TOKEN`). In any real environment replace it with an external
secrets source (ExternalSecrets Operator / Sealed Secrets / SOPS) — do not
commit real credentials. Rotation: `docs/SECRETS.md`.

## Networking

`networkpolicy.yaml` installs a namespace-wide **default-deny** (ingress +
egress) plus explicit allows: in-namespace pod-to-pod, DNS egress, ingress to
`apisix:9080`/`pwa:80` from the `ingress-nginx` namespace, and HTTPS egress
for the PWA's OSM tile fallback. Middleware is expected in the **same
namespace**; if you install it elsewhere, extend the egress allows.

## Ingress / TLS

`ingress.yaml` exposes `api.h2fleet.example.com` → `apisix:9080` and
`app.h2fleet.example.com` → `pwa:80` through an nginx-class controller,
terminating TLS with the `h2fleet-tls` secret. Issue that secret via
cert-manager (recommended) or a manually rotated cert; do not commit certs.

## Middleware (Helm) — pinned versions

Middleware is installed via upstream Helm charts/operators into the same
cluster (service DNS names in the `h2fleet-config` ConfigMap assume the
**h2fleet namespace**). Tested/pinned chart versions:

| Component | Chart | Version | Notes |
|---|---|---|---|
| Kafka | `bitnami/kafka` | `29.3.4` | KRaft or ZK; create the SPEC §3.3 topics or keep auto-create |
| Postgres + TimescaleDB + PostGIS | `timescale/timescaledb-single` — or deploy the `timescale/timescaledb-ha:pg16.4-ts2.17.2-all` image via a StatefulSet | `0.33.1` | needs postgis + timescaledb extensions |
| Redis | `bitnami/redis` | `20.3.0` | AOF on; auth enabled in prod |
| Keycloak | `bitnami/keycloak` | `24.0.5` | import `infra/keycloak/realm-h2fleet.json` (rendered) |
| Temporal | `temporalio/temporal` | `0.54.0` | with its own Postgres |
| APISIX | `apisix/apisix` | `2.10.0` | creates the `apisix` Service the Ingress targets; apply routes from `infra/apisix/apisix.yaml` |
| Permify | `permify/permify` | `0.2.9` | postgres engine, own `permify` database |
| OpenSearch | `opensearch/opensearch` | `2.26.1` | single-node ok for dev |
| MinIO | `minio/minio` (community) | `5.3.0` | buckets `h2-lakehouse`, `h2-open-data`, `h2-backups` |
| Mojaloop | `mojaloop/mojaloop` | `16.0.0` | full switch; see <https://github.com/mojaloop/helm> |
| TigerBeetle | no official chart — run image `ghcr.io/tigerbeetle/tigerbeetle:0.16.13` as a single-replica StatefulSet | — | RWO volume, no rolling updates |
| Prometheus/Grafana/Alertmanager | `prometheus-community/kube-prometheus-stack` | `65.3.1` | import `infra/observability/grafana/dashboards/*.json` |

Pin chart versions in your tooling (`helm upgrade --version <pinned>`);
re-validate upgrades in staging before bumping.

## Per-deployment module toggling

Two levels, deliberately different:

1. **Domain scoping at deploy time** — `TOGGLE_DOMAINS` in the ConfigMap.
   `toggle-service` reads it at bootstrap and only seeds/enables modules whose
   domain is listed (`fleet,infra,citizen,commerce` or `all`). Domains not
   listed are *absent from the deployment*: their routes 404, their consumers
   and Temporal workflows never start. This is the "deployment profile" knob
   (see `docs/DEPLOYMENT.md` → profiles `city-pilot`, `full`).

2. **Module toggles at runtime** — `PUT /api/toggles/v1/toggles/{module}`
   (Keycloak role `platform-admin`). Flips a single module on/off within the
   deployed domains, live, without redeploying. Changes are cached in Redis
   (`toggles:<module>`, TTL 30 s) and broadcast on Kafka topic
   `toggle.changed`; every service picks them up within seconds via its
   toggle-client SDK (5 s local cache, fail-closed).

To scope a dev deployment to fleet + infra only (as `overlays/dev` does):

```bash
kubectl patch configmap h2fleet-config -n h2fleet \
  --type merge -p '{"data":{"TOGGLE_DOMAINS":"fleet,infra"}}'
kubectl rollout restart deploy/toggle-service -n h2fleet
```

## Database migrations

Apply the goose migrations before rolling app Deployments (image
`ghcr.io/kukymbr/goose-docker:3.27.1`, dir `infra/sql/migrations` — see
`infra/sql/migrations/README.md`), e.g. as a Helm pre-install Job or
`kubectl run` one-shot. The compose stack runs the same migrator as the
`migrator` one-shot service.

## Adding more services

Copy `base/fleet-api.yaml`, change name/port/image per the SPEC §3.6 port map
(8080 toggle, 8081 fleet, 8082 infra, 8083 citizen, 8084 commerce, 8090 ml,
8091 optimize, 8092 twin, 8093 ingest, 8094 carbon) and add the file plus a
PDB/NetworkPolicy entry to `base/kustomization.yaml`.
