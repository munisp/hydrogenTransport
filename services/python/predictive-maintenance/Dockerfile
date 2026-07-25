# syntax=docker/dockerfile:1
# Build context: repository root (needs packages/toggle-client/python and services/python/shared).
FROM python:3.12-slim AS builder

WORKDIR /build
COPY packages/toggle-client/python /build/toggle-client
COPY services/python/shared /build/shared
COPY services/python/predictive-maintenance/requirements.txt ./requirements.txt
# CPU-only torch wheel first (loads the ml-platform LSTM artifact), then the rest.
RUN pip install --no-cache-dir --prefix=/install torch==2.5.1 \
        --index-url https://download.pytorch.org/whl/cpu \
    && pip install --no-cache-dir --prefix=/install -r requirements.txt \
    && pip install --no-cache-dir --prefix=/install /build/toggle-client \
    && pip install --no-cache-dir --prefix=/install /build/shared

FROM python:3.12-slim
ENV PYTHONUNBUFFERED=1 PYTHONDONTWRITEBYTECODE=1
COPY --from=builder /install /usr/local
RUN useradd --system --uid 10001 app
WORKDIR /srv
COPY services/python/predictive-maintenance/app ./app
COPY services/python/predictive-maintenance/train.py ./train.py
RUN mkdir -p models && chown -R app:app /srv
USER app
EXPOSE 8090
ENV PORT=8090
HEALTHCHECK --interval=15s --timeout=3s --start-period=10s \
    CMD python -c "import os,urllib.request;urllib.request.urlopen(f\"http://127.0.0.1:{os.environ.get('PORT','8090')}/healthz\",timeout=3)" || exit 1
CMD ["sh", "-c", "uvicorn app.main:app --host 0.0.0.0 --port ${PORT}"]
