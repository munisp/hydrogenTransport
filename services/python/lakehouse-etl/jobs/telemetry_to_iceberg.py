"""Job: Postgres fleet.telemetry -> Iceberg table lakehouse.fleet.telemetry_raw.

Usage (see README for full spark-submit with --packages):
    spark-submit jobs/telemetry_to_iceberg.py [--full] [--since-hours 24]

Default mode is incremental (last --since-hours of telemetry, idempotent via
Iceberg `overwrite` of affected day partitions is approximated with a dynamic
`INSERT OVERWRITE` on touched partitions; use --full for a rebuild).
"""

from __future__ import annotations

import argparse

from common import build_spark, init_sedona, jdbc_config

TABLE = "lakehouse.fleet.telemetry_raw"


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--full", action="store_true", help="rebuild the whole table")
    parser.add_argument("--since-hours", type=int, default=24, help="incremental window")
    args = parser.parse_args()

    spark = build_spark("h2fleet-telemetry-to-iceberg")
    init_sedona(spark)
    jdbc = jdbc_config()

    where = "true" if args.full else f"ts > now() - interval '{args.since_hours} hours'"
    dbtable = (
        "(SELECT bus_id, ts, speed_kph, h2_level_pct, fuel_cell_kw, battery_soc_pct,"
        "       odometer_km, ST_Y(geom) AS lat, ST_X(geom) AS lon"
        f" FROM fleet.telemetry WHERE {where}) t"
    )

    df = (
        spark.read.format("jdbc")
        .option("url", jdbc.url)
        .option("user", jdbc.user)
        .option("password", jdbc.password)
        .option("driver", jdbc.driver)
        .option("dbtable", dbtable)
        .option("fetchsize", "10000")
        .load()
    )

    spark.sql("CREATE NAMESPACE IF NOT EXISTS lakehouse.fleet")
    spark.sql(
        f"""
        CREATE TABLE IF NOT EXISTS {TABLE} (
            bus_id STRING, ts TIMESTAMP, speed_kph DOUBLE, h2_level_pct DOUBLE,
            fuel_cell_kw DOUBLE, battery_soc_pct DOUBLE, odometer_km DOUBLE,
            lat DOUBLE, lon DOUBLE
        )
        USING iceberg
        PARTITIONED BY (days(ts))
        TBLPROPERTIES ('write.format.default' = 'parquet')
        """
    )

    df.createOrReplaceTempView("src")
    # INSERT OVERWRITE on a partitioned Iceberg table replaces only the
    # partitions present in `src`, so incremental windows stay idempotent.
    spark.sql(f"INSERT OVERWRITE {TABLE} SELECT * FROM src")
    count = spark.sql(f"SELECT count(*) AS c FROM src").first()["c"]
    print(f"[telemetry_to_iceberg] wrote {count} rows to {TABLE} (full={args.full})")
    spark.stop()


if __name__ == "__main__":
    main()
