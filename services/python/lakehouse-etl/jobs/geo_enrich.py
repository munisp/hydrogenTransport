"""Job: geospatial enrichment of lakehouse telemetry with Sedona.

Reads lakehouse.fleet.telemetry_raw, builds Sedona points, and spatial-joins
against:
  * depot zones      (fleet.depot_zones(id, name, geom) when present, else a
                      deterministic synthetic set covering the operating area)
  * route corridors  (fleet.route_corridors(id, name, geom); same fallback)

Result is written to lakehouse.fleet.telemetry_geo (Iceberg, partitioned by
days(ts)) with columns route_id / depot_id attached (NULL when outside all
zones/corridors).

Usage: spark-submit jobs/geo_enrich.py [--since-days 1] [--full]
"""

from __future__ import annotations

import argparse

from common import build_spark, init_sedona, jdbc_config

SRC_TABLE = "lakehouse.fleet.telemetry_raw"
DST_TABLE = "lakehouse.fleet.telemetry_geo"

# Deterministic synthetic fallback zones (lon/lat, operating area ~ Berlin).
# A circle-ish polygon per depot, and 3 corridor polylines buffered to ~500 m.
_SYNTHETIC_DEPOTS = [
    ("DEPOT-CENTRAL", "Central Depot", 13.4050, 52.5200, 0.02),
    ("DEPOT-NORTH", "North Depot", 13.3900, 52.5800, 0.02),
    ("DEPOT-SOUTH", "South Depot", 13.4200, 52.4600, 0.02),
]
_SYNTHETIC_CORRIDORS = [
    ("R12", "Ring 12", "LINESTRING(13.30 52.52, 13.50 52.52)", 0.005),
    ("R45", "North-South 45", "LINESTRING(13.405 52.44, 13.405 52.60)", 0.005),
    ("R7", "Diagonal 7", "LINESTRING(13.32 52.46, 13.49 52.58)", 0.005),
]


def _polygon_wkt(cx: float, cy: float, r: float, n: int = 16) -> str:
    import math

    pts = [
        f"{cx + r * math.cos(2 * math.pi * i / n):.6f} {cy + r * math.sin(2 * math.pi * i / n):.6f}"
        for i in range(n + 1)
    ]
    return f"POLYGON(({', '.join(pts)}))"


def load_zones(spark, jdbc) -> tuple[bool, bool]:
    """Create temp views depot_zones(id, name, geom) and
    route_corridors(id, name, geom). Returns (depots_from_db, corridors_from_db)."""

    def try_load(table: str, id_col: str, name_col: str) -> bool:
        try:
            df = (
                spark.read.format("jdbc")
                .option("url", jdbc.url)
                .option("user", jdbc.user)
                .option("password", jdbc.password)
                .option("driver", jdbc.driver)
                .option("dbtable", f"(SELECT {id_col}::text AS id, {name_col} AS name,"
                                   f" ST_AsText(geom) AS wkt FROM {table}) t")
                .load()
            )
            if df.rdd.isEmpty():
                return False
            df.createOrReplaceTempView(f"{table}_raw")
            spark.sql(
                f"CREATE OR REPLACE TEMP VIEW {table} AS "
                f"SELECT id, name, ST_GeomFromWKT(wkt) AS geom FROM {table}_raw"
            )
            return True
        except Exception as exc:  # table does not exist yet -> fallback
            print(f"[geo_enrich] {table} unavailable ({exc}); using synthetic zones")
            return False

    depots_db = try_load("fleet.depot_zones", "id", "name")
    if not depots_db:
        rows = [(i, n, _polygon_wkt(x, y, r)) for i, n, x, y, r in _SYNTHETIC_DEPOTS]
        spark.createDataFrame(rows, "id STRING, name STRING, wkt STRING").createOrReplaceTempView("depot_raw")
        spark.sql(
            "CREATE OR REPLACE TEMP VIEW depot_zones AS "
            "SELECT id, name, ST_GeomFromWKT(wkt) AS geom FROM depot_raw"
        )

    corridors_db = try_load("fleet.route_corridors", "id", "name")
    if not corridors_db:
        rows = [(i, n, wkt, buf) for i, n, wkt, buf in _SYNTHETIC_CORRIDORS]
        spark.createDataFrame(rows, "id STRING, name STRING, wkt STRING, buf DOUBLE").createOrReplaceTempView("corr_raw")
        spark.sql(
            "CREATE OR REPLACE TEMP VIEW route_corridors AS "
            "SELECT id, name, ST_Buffer(ST_GeomFromWKT(wkt), buf) AS geom FROM corr_raw"
        )
    return depots_db, corridors_db


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--full", action="store_true", help="rebuild the whole table")
    parser.add_argument("--since-days", type=int, default=1, help="incremental window")
    args = parser.parse_args()

    spark = build_spark("h2fleet-geo-enrich")
    init_sedona(spark)
    jdbc = jdbc_config()

    depots_db, corridors_db = load_zones(spark, jdbc)

    where = "" if args.full else f"WHERE ts > current_timestamp() - interval {args.since_days} days"
    spark.sql(f"SELECT * FROM {SRC_TABLE} {where}").createOrReplaceTempView("telemetry_src")
    spark.sql(
        """
        CREATE OR REPLACE TEMP VIEW telemetry_pts AS
        SELECT *, ST_Point(lon, lat) AS pt FROM telemetry_src
        """
    )

    spark.sql("CREATE NAMESPACE IF NOT EXISTS lakehouse.fleet")
    spark.sql(
        f"""
        CREATE TABLE IF NOT EXISTS {DST_TABLE} (
            bus_id STRING, ts TIMESTAMP, speed_kph DOUBLE, h2_level_pct DOUBLE,
            fuel_cell_kw DOUBLE, battery_soc_pct DOUBLE, odometer_km DOUBLE,
            lat DOUBLE, lon DOUBLE, route_id STRING, depot_id STRING
        )
        USING iceberg
        PARTITIONED BY (days(ts))
        TBLPROPERTIES ('write.format.default' = 'parquet')
        """
    )

    # Point-in-polygon for depots; nearest corridor within its buffer for routes.
    spark.sql(
        f"""
        INSERT OVERWRITE {DST_TABLE}
        SELECT
            t.bus_id, t.ts, t.speed_kph, t.h2_level_pct, t.fuel_cell_kw,
            t.battery_soc_pct, t.odometer_km, t.lat, t.lon,
            r.route_id, d.depot_id
        FROM telemetry_pts t
        LEFT JOIN (
            SELECT b, tstamp, min(route_id) AS route_id FROM (
                SELECT pt.bus_id AS b, pt.ts AS tstamp, c.id AS route_id
                FROM telemetry_pts pt JOIN route_corridors c ON ST_Within(pt.pt, c.geom)
            ) GROUP BY b, tstamp
        ) r ON r.b = t.bus_id AND r.tstamp = t.ts
        LEFT JOIN (
            SELECT b, tstamp, min(depot_id) AS depot_id FROM (
                SELECT pt.bus_id AS b, pt.ts AS tstamp, z.id AS depot_id
                FROM telemetry_pts pt JOIN depot_zones z ON ST_Within(pt.pt, z.geom)
            ) GROUP BY b, tstamp
        ) d ON d.b = t.bus_id AND d.tstamp = t.ts
        """
    )
    count = spark.sql("SELECT count(*) AS c FROM telemetry_src").first()["c"]
    print(
        f"[geo_enrich] enriched {count} rows -> {DST_TABLE} "
        f"(depots_db={depots_db}, corridors_db={corridors_db}, full={args.full})"
    )
    spark.stop()


if __name__ == "__main__":
    main()
