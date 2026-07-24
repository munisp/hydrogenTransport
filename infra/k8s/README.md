# H2Fleet on Kubernetes (kustomize)

Kustomize layout for deploying H2Fleet to Kubernetes. The **base** contains the
namespace, the shared `h2fleet-config` ConfigMap and example workloads
(`toggle-service`, `fleet-api`). Overlays tailor a deployment to a domain
subset — this is how a *per-deployment profile* (e.g. a city pilot running only
fleet + infra) is expressed in k8s.

```
infra/k8s/
  base/                 namespace, configmap, example deployments + services
  overlays/dev/         dev overlay: TOGGLE_DOMAINS=fleet,infra, 1 replica, :dev images
```

## Apply

```bash
kubectl apply -k infra/k8s/overlays/dev        # dev / city-pilot profile
kubectl apply -k infra/k8s/base                # all domains (TOGGLE_DOMAINS=all)
kubectl kustomize infra/k8s/overlays/dev       # render without applying
```

Middleware (Kafka, Postgres/Timescale, Redis, Keycloak, Permify, Temporal,
APISIX, TigerBeetle, MinIO, OpenSearch, Mojaloop) is expected to be installed
via its upstream operators/Helm charts into the same cluster; the ConfigMap
values (`DATABASE_URL`, `KAFKA_BROKERS`, …) point at in-cluster service DNS
names. The full Mojaloop switch chart is at <https://github.com/mojaloop/helm>.

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

## Adding more services

Copy `base/fleet-api.yaml`, change name/port/image per the SPEC §3.6 port map
(8080 toggle, 8081 fleet, 8082 infra, 8083 citizen, 8084 commerce, 8090 ml,
8091 optimize, 8092 twin) and add the file to `base/kustomization.yaml`.
