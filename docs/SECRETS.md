# H2Fleet Secrets — inventory & rotation

All compose credentials use `${VAR:-dev-default}` interpolation; override via
`.env` (template: `.env.example`, never commit a filled `.env`). The committed
defaults are development-only and are intentionally weak.

## Inventory

| Secret | Env var | Used by | Dev default | Rotation |
|---|---|---|---|---|
| Main Postgres password | `POSTGRES_PASSWORD` | postgres, all services' `DATABASE_URL`, backup, permify init/migrate, goose migrator | `h2pass` | quarterly / on leak |
| Temporal PG password | `TEMPORAL_POSTGRES_PASSWORD` | postgres-temporal, temporal, backup | `temporal` | quarterly |
| Keycloak console admin | `KEYCLOAK_ADMIN` / `KEYCLOAK_ADMIN_PASSWORD` | keycloak | `admin`/`admin` | on any admin change |
| `services` client secret | `KEYCLOAK_SERVICES_CLIENT_SECRET` | realm import (rendered), service-to-service token flows | `h2fleet-services-secret-change-me` | quarterly |
| Test user passwords | `KEYCLOAK_{ADMIN_USER,OPERATOR,DRIVER,CITIZEN}_PASSWORD` | realm import | `admin123` etc. | with realm re-import |
| MinIO root creds | `MINIO_ROOT_USER` / `MINIO_ROOT_PASSWORD` | minio, minio-init, iceberg-rest, lakehouse-etl, backup | `h2admin`/`h2adminpass` | quarterly |
| APISIX admin key | `APISIX_ADMIN_KEY` | apisix (admin API, in-network only) | `h2fleet-admin-key-change-me` | quarterly |
| Leak sensor ingest token | `LEAK_INGEST_TOKEN` | infra-api (`POST /v1/safety/leak`) | `dev-leak-token-change-me` | per sensor-fleet rollout |
| Grafana admin | `GRAFANA_ADMIN_USER` / `GRAFANA_ADMIN_PASSWORD` | grafana | `admin`/`admin` | on any admin change |
| Backup schedule/retention | `BACKUP_INTERVAL_SECONDS`, `BACKUP_RETENTION_DAYS` | backup | 24 h / 14 d | n/a |

## Notes & constraints

* **KEYCLOAK_AUDIENCE**: services validate JWT `iss` + signature and accept
  both the browser and in-network issuers (`KEYCLOAK_ISSUER_ALT`); `aud` is
  not strictly pinned in dev. In prod, set the expected audience (e.g. the
  `services` client id or a dedicated resource server) per service.
* **Realm rendering**: `infra/keycloak/realm-h2fleet.json` is a *template*
  with `${VAR}` placeholders. The `keycloak-realm-init` one-shot renders it
  with sed (`infra/keycloak/substitute-realm.sh`) into a compose volume
  before `keycloak --import-realm` reads it. Values must not contain
  `&`, `|` or newlines (sed metacharacters — the script refuses them).
* **k8s**: `infra/k8s/base/secret.yaml` holds dev values only; replace with
  ExternalSecrets/Sealed Secrets/SOPS in real clusters.
* **gitleaks** runs in CI to catch accidental commits of real values.

## Rotation procedure (compose)

1. Set the new value in `.env`.
2. Postgres passwords additionally require changing the live role:
   `docker exec -i h2-postgres psql -U h2 -d h2fleet -c "ALTER ROLE h2 PASSWORD '<new>';"`.
3. Keycloak realm secrets additionally require a re-import (dev realm is
   re-rendered on each fresh boot; with existing `keycloak_data`, rotate via
   the admin console instead).
4. `$COMPOSE up -d --force-recreate <affected services>`; verify
   `make gateway-check` and a login round-trip.
5. Update the ops secret store of record; delete the old value from shell
   history/CI logs.
