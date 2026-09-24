# Wave 11 — comprehensive performance tuning (all services, gateway, mobile)

Scope: end-to-end performance tuning across every runtime in the stack —
Go services, Rust services, Python services, the TypeScript analytics BFF,
the APISIX gateway, and the React Native mobile app — targeting industry
standard response times (p50 < 50 ms, p99 < 300 ms on API surfaces; mobile
first paint < 1.5 s p50). Full latency budgets, measurement notes and the
per-layer rationale live in `docs/PERFORMANCE_TUNING.md`; this file records
the wave, its changes, and gate results.

Toolchains installed for this wave: Go 1.26.4, Rust 1.98.1 (+cmake 4.2.1
for rdkafka-sys), Node 20.20.2, Python 3.12.12 with all pinned deps from
each service's `requirements.txt`.

## Findings → fixes

| # | Severity | Finding | Fix |
|---|----------|---------|-----|
| W11-1 | high | **Connection-pool churn in every Go service**: services opened database connections per-request or with driver defaults, so burst traffic paid full TCP+TLS+auth handshakes in the request path. | New shared `packages/go-db` (`dbpool.NewPool`: 32 max / 4 min conns, 30 m lifetime, 10 m idle, 1 m healthcheck; env `DB_MAX_CONNS`/`DB_MIN_CONNS`) adopted by all 7 Go services. |
| W11-2 | high | **Unbounded HTTP server phases**: Go servers ran with default ReadHeader/Read/Write/Idle timeouts — a slow or stalled client could pin goroutines and connections indefinitely. | `dbpool.NewServer` (same package): ReadHeader 10 s, Read 15 s, Write 60 s, Idle 90 s, MaxHeaderBytes 16 KiB; wired into every Go service's `cmd/server/main.go`. |
| W11-3 | high | **Outbound keepalive starvation**: every Go outbound call (Keycloak, KPI sources, ops, onboarding captcha, audit-log mirror/audit-client, citizen opendata, commerce Mojaloop, infra webhooks) used `http.DefaultTransport` — MaxIdleConnsPerHost **2**, so concurrent calls to one upstream churned connections. | New shared `packages/go-httpclient.New(timeout)`: MaxIdleConns 256, per-host 64, dial 2 s, keepalive 30 s, HTTP/2 enabled; adopted at ~10 call sites. |
| W11-4 | high | **Gateway keepalive gaps**: 19 APISIX upstreams had no `keepalive_pool`, so edge→service connections were rebuilt per request, adding TCP/TLS handshake latency to every hop. | `keepalive_pool {size: 512, requests: 1000, idle_timeout: 60}` + `timeouts {connect: 2, send: 10, read: 30}` on all 19 upstreams in `infra/apisix/apisix.yaml`. |
| W11-5 | high (mobile) | **Mobile fetch path had no timeout/retry tuning and unwindowed lists**: a stalled API call hung the UI indefinitely; FlatLists rendered full datasets. | `apps/mobile/src/api/client.ts`: AbortController 10 s timeout, GET retry 2× with 300/900 ms backoff. All list screens: `initialNumToRender 12`, `maxToRenderPerBatch 8`, `updateCellsBatchingPeriod 50`, `windowSize 9`, `removeClippedSubviews`. |
| W11-6 | medium | **Python services single-worker pinned**: all 5 FastAPI services ran `uvicorn` with one worker; the fast handlers (health, auth) could not use spare cores even though GIL-bound ML handlers in ml-platform stay single-worker by design (model memory × workers). | Dockerfiles accept `UVICORN_WORKERS` (default **1** — zero behavior change; compose overlay sets it per-service on multi-core nodes). ml-platform README notes the memory tradeoff for raising it. |
| W11-7 | medium | **analytics-bff served uncompressed JSON**: analytics time-series payloads (~MBs) crossed the network uncompressed. | `hono/compress` middleware on all routes (wire-time gzip, ~10× on JSON series); pg pool ceiling made env-configurable (`PGPOOL_MAX`, default 10). |
| W11-8 | low | **Rust runtimes**: already optimal by default (tokio multi-thread worker count = available cores); no code change — deployment guidance documented (pin container CPU limits ≥ worker count; don't oversubscribe). | Documented in `docs/PERFORMANCE_TUNING.md` §Rust. |

## Verified non-gaps (checked this wave, no change needed)

- **Kafka producer/consumer batching**: producer `linger.ms`/`batch.size`
  and consumer fetch tuning already live in `infra/tuning/kafka-server.prod.properties`
  (lz4, socket buffers) and per-service client configs.
- **Postgres / Redis / OpenSearch / TigerBeetle**: production parameter
  sets already in `infra/tuning/` (shared_buffers, effective_cache_size,
  JVM heap, batching guidance).
- **PWA static assets**: brotli pre-compression + cache headers already in
  `infra/tuning/nginx-pwa.conf` and the pwa build.

## Accepted residual (documented)

- **ml-platform model load**: raising `UVICORN_WORKERS` multiplies in-memory
  model copies (torch/sklearn); keep 1 on memory-constrained nodes or
  scale replicas horizontally instead.
- **Go p99 on cold pool**: the first requests after a service start pay
  connection establishment until the pool reaches its minimum (4); the
  gateway keepalive pool absorbs this at the edge.

## Verification (all gates green)

- **Go** (1.26.4): 8/8 modules build + vet + test. Benchmarks:
  `BenchmarkIntakeRequest` 96.9 µs, `BenchmarkCitizenSelfServe` 83.5 µs
  (handler-only, simulated deps) — well inside the p50 < 50 ms budget at
  the service edge.
- **Rust** (1.98.1): digital-twin 25/25, telemetry-ingest 10/10,
  fluvio-edge 2/2 (cmake 4.2.1 for rdkafka-sys; `CARGO_TARGET_DIR` off the
  noexec mount).
- **Python** (3.12.12, pinned requirements): ml-platform 54/54,
  carbon-analytics 26/26, route-optimizer 26/26, predictive-maintenance
  17/17, ocpp-gateway 37/37 (with shared on PYTHONPATH), telemetry-simulator
  8/8 (DATABASE_URL per env contract), shared 10/10.
- **TypeScript** (Node 20.20.2): packages/db typecheck + `drizzle-kit check`
  + 7/7 vitest; analytics-bff typecheck + esbuild bundle + 8/8 vitest
  (re-run after the compress/pool changes); apps/mobile `tsc --noEmit`;
  apps/pwa `tsc --noEmit`.
