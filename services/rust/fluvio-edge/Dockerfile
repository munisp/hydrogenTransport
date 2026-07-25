# fluvio-edge — H2Fleet edge agent (per-bus gateway).
# Multi-stage per SPEC §3.5. Runs ON the bus gateway next to the local Fluvio
# SPU; forwards to the platform Kafka over whatever uplink is available.

# ---- build -----------------------------------------------------------------
# Builder tracks current stable: the committed Cargo.lock resolves current
# transitive crates whose MSRV may exceed older pinned toolchains.
FROM rust:1-bookworm AS build
WORKDIR /src
# rdkafka-sys builds the bundled librdkafka (make/gcc); no cmake needed.
RUN apt-get update && apt-get install -y --no-install-recommends make gcc pkg-config \
    && rm -rf /var/lib/apt/lists/*
COPY Cargo.toml Cargo.lock* ./
COPY src ./src
RUN cargo build --release --locked 2>/dev/null || cargo build --release

# ---- runtime ---------------------------------------------------------------
FROM debian:bookworm-slim
RUN useradd -r -u 10001 -m fluvio-edge \
    && mkdir -p /var/lib/fluvio-edge && chown fluvio-edge /var/lib/fluvio-edge
COPY --from=build /src/target/release/fluvio-edge /usr/local/bin/fluvio-edge
USER fluvio-edge
ENV RUST_LOG=info,fluvio_edge=debug \
    SPOOL_DIR=/var/lib/fluvio-edge \
    PORT=8093
VOLUME ["/var/lib/fluvio-edge"]
EXPOSE 8093
# /healthz stays 200 while the agent is alive and spooling (degraded is a
# normal edge operating mode); check the process + listener only.
HEALTHCHECK --interval=15s --timeout=3s --retries=5 \
  CMD ["bash", "-c", "exec 3<>/dev/tcp/localhost/${PORT:-8093} && printf 'GET /healthz HTTP/1.0\\r\\n\\r\\n' >&3 && grep -q '200 OK' <&3"]
ENTRYPOINT ["/usr/local/bin/fluvio-edge"]
