# infra/backup — H2Fleet backup runner

Containerised backup loop used by the `backup` service in
`infra/docker-compose.yml`.

* `pg_dump -Fc` of **both** Postgres instances (main `h2fleet` DB and the
  Temporal `temporal` DB) → `s3://h2-backups/{postgres,temporal}/`
* Crash-consistent copy of the TigerBeetle data file
  (`/data/0_0.tigerbeetle`, volume-mounted read-only) →
  `s3://h2-backups/tigerbeetle/`. TigerBeetle is a single-writer append-only
  store, so a file copy is safe; for a fully quiescent snapshot stop the
  `tigerbeetle` container first (see `docs/DR.md`).
* Retention: objects older than `BACKUP_RETENTION_DAYS` (default 14) are
  pruned after each run.

```bash
make backup                 # one-off backup now (runs `backup.sh once`)
docker logs -f h2-backup    # follow the scheduled loop
```

Restore drill: `docs/DR.md`. Secret inventory: `docs/SECRETS.md`.
