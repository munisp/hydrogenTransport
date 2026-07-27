# syntax=docker/dockerfile:1
# Build context: repository root (needs services/python/shared for h2fleet_auth).
FROM python:3.12-slim AS builder

WORKDIR /build
COPY services/python/shared /build/shared
COPY services/python/ocpp-gateway/requirements.txt ./requirements.txt
RUN pip install --no-cache-dir --prefix=/install -r requirements.txt \
    && pip install --no-cache-dir --prefix=/install /build/shared

FROM python:3.12-slim
ENV PYTHONUNBUFFERED=1 PYTHONDONTWRITEBYTECODE=1
COPY --from=builder /install /usr/local
RUN useradd --system --uid 10001 app
WORKDIR /srv
COPY services/python/ocpp-gateway/app ./app
USER app
EXPOSE 8100
ENV PORT=8100
HEALTHCHECK --interval=15s --timeout=3s --start-period=10s \
    CMD python -c "import os,urllib.request;urllib.request.urlopen(f\"http://127.0.0.1:{os.environ.get('PORT','8100')}/healthz\",timeout=3)" || exit 1
CMD ["sh", "-c", "uvicorn app.main:app --host 0.0.0.0 --port ${PORT}"]
