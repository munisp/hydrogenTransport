# syntax=docker/dockerfile:1
# One-shot provisioning job. Build context: repository root (matches the
# docker-compose `opensearch-bootstrap` service entry).
FROM python:3.12-slim

ENV PYTHONUNBUFFERED=1 PYTHONDONTWRITEBYTECODE=1
WORKDIR /srv
COPY services/python/opensearch-bootstrap/requirements.txt ./requirements.txt
RUN pip install --no-cache-dir -r requirements.txt
RUN useradd --system --uid 10001 app
COPY services/python/opensearch-bootstrap/bootstrap.py ./bootstrap.py
USER app
CMD ["python", "bootstrap.py"]
