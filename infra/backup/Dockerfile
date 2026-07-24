# syntax=docker/dockerfile:1
# H2Fleet backup runner: pg_dump (both postgres instances) + TigerBeetle
# data-file snapshot, uploaded to MinIO with the mc client.
# Build context: infra/backup
FROM minio/mc:RELEASE.2024-10-08T09-37-26Z AS mc

FROM postgres:16-alpine
COPY --from=mc /usr/bin/mc /usr/local/bin/mc
COPY backup.sh /usr/local/bin/backup.sh
RUN chmod +x /usr/local/bin/backup.sh
# Args: "loop" (default, every BACKUP_INTERVAL_SECONDS) or "once".
ENTRYPOINT ["/usr/local/bin/backup.sh"]
CMD ["loop"]
