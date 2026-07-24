# H2Fleet Disaster Recovery — backup & restore drill

## What is backed up (by the `backup` compose service)

| Artifact | Method | Location (MinIO `h2-backups`) | RPO |
|---|---|---|---|
| Main DB (`h2fleet`) | `pg_dump -Fc` | `postgres/h2fleet_<ts>.dump` | `BACKUP_INTERVAL_SECONDS` (default 24 h) |
| Temporal DB | `pg_dump -Fc` | `temporal/temporal_<ts>.dump` | same |
| TigerBeetle ledger | crash-consistent copy of `0_0.tigerbeetle` | `tigerbeetle/0_0_<ts>.tigerbeetle` | same |

Retention: `BACKUP_RETENTION_DAYS` (default 14) pruned after each run.
One-off run: `make backup`. Config: `infra/backup/`, schedule in
`infra/docker-compose.yml` (`backup` service).

**Not covered by dumps** (rebuild instead): Kafka topics (re-drivable event
log; topics auto-create), Redis (caches + twin hot state rebuild from the
telemetry stream), OpenSearch indexes (rebuilt by lakehouse-etl / bootstrap),
lakehouse Iceberg tables (re-run `make etl`). Keycloak realm is in
`infra/keycloak/realm-h2fleet.json` + `.env` (re-imported).

## TigerBeetle snapshot caveat

TigerBeetle is a single-writer append-only store, so copying the data file
while it runs is crash-consistent (recovery replays the tail on start). For a
fully quiescent snapshot: `$COMPOSE stop tigerbeetle`, run `make backup`,
`$COMPOSE start tigerbeetle`.

## Restore drill (quarterly — time it; target RTO < 1 h)

```bash
# 0. Pick a backup set (TS = timestamp of the artifacts)
TS=20250115T020000Z
mc alias set local http://127.0.0.1:9000 $MINIO_ROOT_USER $MINIO_ROOT_PASSWORD
mc cp local/h2-backups/postgres/h2fleet_${TS}.dump   /tmp/
mc cp local/h2-backups/temporal/temporal_${TS}.dump  /tmp/
mc cp local/h2-backups/tigerbeetle/0_0_${TS}.tigerbeetle /tmp/

# 1. Stop writers
docker compose -f infra/docker-compose.yml --profile all --profile apps \
  stop telemetry-simulator telemetry-ingest digital-twin \
  toggle-service fleet-api infra-api citizen-api commerce-api \
  predictive-maintenance route-optimizer carbon-analytics temporal

# 2. Restore main DB (drop/recreate in DEV; in prod restore to a fresh instance)
docker exec -i h2-postgres psql -U h2 -d postgres \
  -c "SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname='h2fleet' AND pid<>pg_backend_pid();"
docker exec -i h2-postgres psql -U h2 -d postgres -c "DROP DATABASE h2fleet;" \
  -c "CREATE DATABASE h2fleet;"
docker cp /tmp/h2fleet_${TS}.dump h2-postgres:/tmp/restore.dump
docker exec -i h2-postgres pg_restore -U h2 -d h2fleet --no-owner /tmp/restore.dump

# 3. Restore Temporal DB (same pattern against h2-postgres-temporal)
docker cp /tmp/temporal_${TS}.dump h2-postgres-temporal:/tmp/restore.dump
docker exec -i h2-postgres-temporal psql -U temporal -d postgres \
  -c "DROP DATABASE temporal;" -c "CREATE DATABASE temporal;"
docker exec -i h2-postgres-temporal pg_restore -U temporal -d temporal --no-owner /tmp/restore.dump

# 4. Restore TigerBeetle (service MUST be stopped; replace the data file)
docker compose -f infra/docker-compose.yml stop tigerbeetle
docker run --rm -v h2fleet_tigerbeetle_data:/data -v /tmp:/restore alpine \
  sh -c "cp /restore/0_0_${TS}.tigerbeetle /data/0_0.tigerbeetle"
docker compose -f infra/docker-compose.yml start tigerbeetle

# 5. Re-apply migrations + restart platform
make migrate
docker compose -f infra/docker-compose.yml --profile apps up -d

# 6. Verify
make gateway-check
# spot checks: vehicles count = 50, latest telemetry ts <= backup ts,
# ledger balances match commerce.fare_payments sums, one login via Keycloak.
```

## Success criteria for the drill

* RTO (stop → gateway-check green) < 1 h documented in the ops log.
* `SELECT count(*) FROM fleet.vehicles` = 50; `max(ts)` in `fleet.telemetry`
  ≤ backup timestamp.
* TigerBeetle account balances reconcile with `commerce.fare_payments`
  (the ledger is authoritative; investigate any divergence before reopening
  payments).
* Temporal UI shows namespaces; in-flight workflows resume or are visibly
  failed (re-drive per RUNBOOK §3).

## Loss scenarios & priorities

1. **pg_data lost** → sections 1–2 above (RPO = last backup).
2. **tigerbeetle_data lost** → section 4; without any TB backup, rebuild the
   ledger by replaying `commerce.fare_payments` / `commerce.trades` as
   double-entry transfers (contact the commerce owner — script pending).
3. **Whole host lost** → fresh `make up`, restore per above, re-import realm
   (automatic), re-run `make etl` for the lakehouse, re-run
   opensearch-bootstrap. Kafka/Redis left empty intentionally.
