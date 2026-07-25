# syntax=docker/dockerfile:1
# Build context: repository root (needs services/python/shared for h2fleet_auth).
FROM python:3.12-slim AS builder

WORKDIR /build
COPY services/python/shared /build/shared
COPY services/python/ml-platform/requirements.txt ./requirements.txt
# CPU-only torch wheel first (keeps the image small), then the rest.
RUN pip install --no-cache-dir --prefix=/install torch==2.6.0 \
        --index-url https://download.pytorch.org/whl/cpu \
    && pip install --no-cache-dir --prefix=/install -r requirements.txt \
    && pip install --no-cache-dir --prefix=/install /build/shared

FROM python:3.12-slim
ENV PYTHONUNBUFFERED=1 PYTHONDONTWRITEBYTECODE=1
COPY --from=builder /install /usr/local
RUN useradd --system --uid 10001 app
WORKDIR /srv
COPY services/python/ml-platform/app ./app
COPY services/python/ml-platform/models ./models
COPY services/python/ml-platform/data ./data
COPY services/python/ml-platform/training ./training
COPY services/python/ml-platform/artifacts ./artifacts
RUN chown -R app:app /srv
USER app
EXPOSE 8095
ENV PORT=8095 MODEL_ARTIFACTS_DIR=/srv/artifacts
HEALTHCHECK --interval=15s --timeout=3s --start-period=15s \
    CMD python -c "import os,urllib.request;urllib.request.urlopen(f\"http://127.0.0.1:{os.environ.get('PORT','8095')}/healthz\",timeout=3)" || exit 1
CMD ["sh", "-c", "uvicorn app.main:app --host 0.0.0.0 --port ${PORT}"]
