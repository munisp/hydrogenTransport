# syntax=docker/dockerfile:1
# Build context: repository root (consistent with the other Python services).
FROM python:3.12-slim AS builder

WORKDIR /build
COPY services/python/telemetry-simulator/requirements.txt ./requirements.txt
RUN pip install --no-cache-dir --prefix=/install -r requirements.txt

FROM python:3.12-slim
ENV PYTHONUNBUFFERED=1 PYTHONDONTWRITEBYTECODE=1
COPY --from=builder /install /usr/local
RUN useradd --system --uid 10001 app
WORKDIR /srv
COPY services/python/telemetry-simulator/app ./app
USER app
# No HTTP port: the simulator is a headless producer. Health = heartbeat file
# written each tick (see app/simulator.py); checked for freshness here.
HEALTHCHECK --interval=20s --timeout=5s --start-period=60s \
    CMD python -c "import os,sys,time;sys.exit(0 if time.time()-os.path.getmtime('/tmp/sim-heartbeat') < 3*float(os.environ.get('SIM_INTERVAL_SECONDS','5'))+30 else 1)"
CMD ["python", "-m", "app.main"]
