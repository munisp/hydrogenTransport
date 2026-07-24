# syntax=docker/dockerfile:1
FROM rust:1-slim AS builder

RUN apt-get update && apt-get install -y --no-install-recommends \
        build-essential cmake pkg-config libssl-dev libsasl2-dev zlib1g-dev perl \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /app
COPY Cargo.toml Cargo.lock* ./
RUN mkdir -p src && echo 'fn main() {}' > src/main.rs \
    && cargo build --release \
    && rm -rf src target/release/digital-twin*
COPY src ./src
RUN touch src/main.rs && cargo build --release \
    && strip target/release/digital-twin

FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends \
        ca-certificates curl libssl3 libsasl2-2 zlib1g \
    && rm -rf /var/lib/apt/lists/* \
    && useradd --system --uid 10001 app
COPY --from=builder /app/target/release/digital-twin /usr/local/bin/digital-twin
USER app
EXPOSE 8092
ENV PORT=8092
HEALTHCHECK --interval=15s --timeout=3s --start-period=10s \
    CMD curl -fsS "http://127.0.0.1:${PORT}/healthz" || exit 1
ENTRYPOINT ["digital-twin"]
