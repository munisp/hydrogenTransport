"""Shared Spark session factory: Iceberg (Hadoop catalog on MinIO s3a) + Sedona.

Env configuration:
    MINIO_ENDPOINT    http://minio:9000
    MINIO_ACCESS_KEY  / MINIO_SECRET_KEY
    LAKEHOUSE_BUCKET  lakehouse          (warehouse at s3a://lakehouse/warehouse)
    DATABASE_URL      postgresql://user:pass@host:5432/h2fleet
"""

from __future__ import annotations

import os
from dataclasses import dataclass

from pyspark.sql import SparkSession


@dataclass(frozen=True)
class JdbcConfig:
    url: str
    user: str
    password: str
    driver: str = "org.postgresql.Driver"


def jdbc_config() -> JdbcConfig:
    dsn = os.environ.get("DATABASE_URL", "postgresql://postgres:postgres@localhost:5432/h2fleet")
    # postgresql://user:pass@host:port/db  ->  jdbc:postgresql://host:port/db
    without_scheme = dsn.split("://", 1)[1]
    creds, hostpart = without_scheme.split("@", 1)
    user, password = creds.split(":", 1)
    return JdbcConfig(url=f"jdbc:postgresql://{hostpart}", user=user, password=password)


def build_spark(app_name: str) -> SparkSession:
    endpoint = os.environ.get("MINIO_ENDPOINT", "http://localhost:9000")
    access_key = os.environ.get("MINIO_ACCESS_KEY", "minioadmin")
    secret_key = os.environ.get("MINIO_SECRET_KEY", "minioadmin")
    bucket = os.environ.get("LAKEHOUSE_BUCKET", "lakehouse")

    return (
        SparkSession.builder.appName(app_name)
        # --- Iceberg ---
        .config(
            "spark.sql.extensions",
            "org.apache.iceberg.spark.extensions.IcebergSparkSessionExtensions",
        )
        .config("spark.sql.catalog.lakehouse", "org.apache.iceberg.spark.SparkCatalog")
        .config("spark.sql.catalog.lakehouse.type", "hadoop")
        .config("spark.sql.catalog.lakehouse.warehouse", f"s3a://{bucket}/warehouse")
        .config("spark.sql.defaultCatalog", "lakehouse")
        # --- MinIO via s3a ---
        .config("spark.hadoop.fs.s3a.endpoint", endpoint)
        .config("spark.hadoop.fs.s3a.access.key", access_key)
        .config("spark.hadoop.fs.s3a.secret.key", secret_key)
        .config("spark.hadoop.fs.s3a.path.style.access", "true")
        .config("spark.hadoop.fs.s3a.impl", "org.apache.hadoop.fs.s3a.S3AFileSystem")
        .config("spark.hadoop.fs.s3a.connection.ssl.enabled", "false")
        .config("spark.hadoop.fs.s3a.aws.credentials.provider",
                "org.apache.hadoop.fs.s3a.SimpleAWSCredentialsProvider")
        .getOrCreate()
    )


def init_sedona(spark: SparkSession) -> None:
    """Register Sedona SQL functions (ST_Point, ST_Within, ST_DWithin, ...).
    Handles both the >=1.6 `sedona.spark.SedonaContext` API and the older
    `sedona.register.SedonaRegistrator`."""
    try:
        from sedona.spark import SedonaContext  # apache-sedona >= 1.6

        SedonaContext.create(spark)
        return
    except ImportError:
        pass
    from sedona.register import SedonaRegistrator  # apache-sedona < 1.6

    SedonaRegistrator.registerAll(spark)
