# syntax=docker/dockerfile:1
# Spark driver image for the lakehouse ETL jobs. Build context: repo root.
FROM python:3.12-slim

RUN apt-get update && apt-get install -y --no-install-recommends \
        default-jre-headless procps curl \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /opt/spark-jobs
COPY services/python/lakehouse-etl/requirements.txt ./requirements.txt
RUN pip install --no-cache-dir -r requirements.txt

COPY services/python/lakehouse-etl/jobs ./jobs

ENV PYTHONUNBUFFERED=1 \
    PYSPARK_PYTHON=python3 \
    ICEBERG=org.apache.iceberg:iceberg-spark-runtime-3.5_2.12:1.6.1 \
    SEDONA=org.apache.sedona:sedona-spark-shaded-3.5_2.12:1.6.1 \
    GEOTOOLS=org.datasyslab:geotools-wrapper:1.6.1-28.2 \
    AWS=org.apache.hadoop:hadoop-aws:3.3.4,com.amazonaws:aws-java-sdk-bundle:1.12.262 \
    PG=org.postgresql:postgresql:42.7.4

# Usage: docker run ... h2fleet/lakehouse-etl jobs/telemetry_to_iceberg.py --full
ENTRYPOINT ["sh", "-c", "spark-submit --packages \"$ICEBERG,$SEDONA,$GEOTOOLS,$AWS,$PG\" \"$@\"", "--"]
CMD ["jobs/telemetry_to_iceberg.py", "--since-hours", "24"]
