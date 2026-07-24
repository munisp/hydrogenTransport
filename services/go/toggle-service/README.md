# toggle-service

Feature-toggle control plane for H2Fleet (SPEC §3.2). Port **8080**
(gateway prefix `/api/toggles/*`).

## API

| Method | Path                  | Auth                          | Response |
|--------|-----------------------|-------------------------------|----------|
| GET    | `/v1/toggles`         | none                          | `{ "toggles": { "<module>": bool, ... } }` |
| GET    | `/v1/toggles/{module}`| none (Redis-cached 30s)       | `{ "module", "enabled", "domain" }` |
| PUT    | `/v1/toggles/{module}`| Keycloak JWT, role `platform-admin` | `{ "module", "enabled", "domain" }` |
| GET    | `/healthz`            | none                          | `{ "status": "ok" }` |

- Storage: Postgres `feature_toggles(module text pk, domain text, enabled bool, updated_at timestamptz)`
- Cache: Redis key `toggles:<module>`, TTL **30s**
- On `PUT` publishes `toggle.changed` (CloudEvents-ish envelope, SPEC §3.3) to Kafka
- On startup seeds all **20 modules** (`INSERT ... ON CONFLICT DO NOTHING`, default enabled)

## Configuration (env, SPEC §3.5)

| Var               | Required | Default | Notes |
|-------------------|----------|---------|-------|
| `PORT`            | no       | `8080`  | HTTP port |
| `DATABASE_URL`    | yes      | —       | Postgres DSN |
| `REDIS_ADDR`      | no       | —       | `host:port`; cache disabled when unset |
| `KAFKA_BROKERS`   | no       | —       | comma-separated; no-op logging publisher when unset |
| `KEYCLOAK_ISSUER` | no       | —       | e.g. `http://keycloak:8080/realms/h2fleet`; PUT fails closed (503/401) when unset |

## Run

```sh
go run ./cmd/server
# or
docker build -t h2fleet/toggle-service .
docker run -p 8080:8080 -e DATABASE_URL=postgres://... h2fleet/toggle-service
```
