# lakehouse-etl (Spark 3.5 + Apache Sedona → Iceberg on MinIO)

Batch geospatial ETL into the analytics zone (SPEC §3.8: Apache Sedona/Spark, Lakehouse
= Iceberg on MinIO). Not a long-running service: jobs run via `spark-submit`
(cron / k8s CronJob / Temporal activity).

## Jobs

| job | source → sink |
|---|---|
| `jobs/telemetry_to_iceberg.py` | Postgres `fleet.telemetry` → `lakehouse.fleet.telemetry_raw` (Iceberg, partitioned by `days(ts)`) |
| `jobs/geo_enrich.py` | `lakehouse.fleet.telemetry_raw` ⊲⊳ depot zones + route corridors (Sedona `ST_Within`) → `lakehouse.fleet.telemetry_geo` |

- Incremental by default (`--since-hours 24` / `--since-days 1`), idempotent: Iceberg
  `INSERT OVERWRITE` replaces only touched day-partitions. `--full` rebuilds.
- Zone sources: `fleet.depot_zones(id, name, geom)` / `fleet.route_corridors(id, name, geom)`
  when present; otherwise a deterministic synthetic set for the operating area
  (documented in `jobs/geo_enrich.py`).

## Required jars (Spark 3.5, Scala 2.12)

```
ICEBERG=org.apache.iceberg:iceberg-spark-runtime-3.5_2.12:1.6.1
SEDONA=org.apache.sedona:sedona-spark-shaded-3.5_2.12:1.6.1
GEOTOOLS=org.datasyslab:geotools-wrapper:1.6.1-28.2
AWS=org.apache.hadoop:hadoop-aws:3.3.4,com.amazonaws:aws-java-sdk-bundle:1.12.262
PG=org.postgresql:postgresql:42.7.4
```

## spark-submit

```bash
export DATABASE_URL=postgresql://postgres:postgres@postgres:5432/h2fleet
export MINIO_ENDPOINT=http://minio:9000 MINIO_ACCESS_KEY=minioadmin MINIO_SECRET_KEY=minioadmin

# 1) telemetry -> Iceberg (hourly cron)
spark-submit \
  --packages "$ICEBERG,$SEDONA,$GEOTOOLS,$AWS,$PG" \
  --conf spark.sql.extensions=org.apache.iceberg.spark.extensions.IcebergSparkSessionExtensions \
  jobs/telemetry_to_iceberg.py --since-hours 24

# 2) geospatial enrichment (after step 1)
spark-submit \
  --packages "$ICEBERG,$SEDONA,$GEOTOOLS,$AWS,$PG" \
  jobs/geo_enrich.py --since-days 1

# full rebuild
spark-submit --packages "$ICEBERG,$SEDONA,$GEOTOOLS,$AWS,$PG" jobs/telemetry_to_iceberg.py --full
spark-submit --packages "$ICEBERG,$SEDONA,$GEOTOOLS,$AWS,$PG" jobs/geo_enrich.py --full
```

## Query the lakehouse

```sql
-- spark-sql / Trino on the same Hadoop catalog (warehouse s3a://lakehouse/warehouse)
SELECT depot_id, date_trunc('day', ts) d, count(*) pts, max(odometer_km)-min(odometer_km) km
FROM lakehouse.fleet.telemetry_geo
GROUP BY depot_id, date_trunc('day', ts);
```

## Docker (spark driver image)

```bash
docker build -f services/python/lakehouse-etl/Dockerfile -t h2fleet/lakehouse-etl .
docker run --rm --network h2fleet \
  -e DATABASE_URL -e MINIO_ENDPOINT -e MINIO_ACCESS_KEY -e MINIO_SECRET_KEY \
  h2fleet/lakehouse-etl jobs/telemetry_to_iceberg.py --since-hours 24
```
