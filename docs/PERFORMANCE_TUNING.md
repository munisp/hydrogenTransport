# H2Fleet Performance Tuning (Wave 11)

Scope: all Go services, shared packages, the APISIX gateway layer, and the
React Native mobile app. Goal: hold **p50 < 50 ms / p99 < 300 ms** on every
synchronous API surface under the SPEC §2 load model (50 buses, city-scale
burst), with mobile first paint bounded on low-end devices.

## 1. Where the latency actually was

Audit of the pre-tuning code found four dominant, code-level sources of tail
latency — all connection churn, not algorithmic cost:

| # | Finding | Effect |
|---|---------|--------|
| 1 | Every service built its pool with bare `pgxpool.New` (defaults: `MaxConns = NumCPU`, `MinConns = 0`, no health check) | Connection open/close churn on every burst; cold-start pile-up after idle periods |
| 2 | Every `http.Server` set only `ReadHeaderTimeout: 10s` — no `ReadTimeout`/`WriteTimeout`/`IdleTimeout` | Hung clients and slow writers hold goroutines/sockets indefinitely; no connection reuse bound |
| 3 | All outbound HTTP clients (Keycloak, ops, KPI, OpenSearch, Mojaloop, toggle, webhooks, anomaly scorer, audit sink) used the default `http.Transport` — `MaxIdleConnsPerHost: 2` | Any burst of >2 concurrent calls to one upstream opens fresh TCP+TLS handshakes per request — the main p99 jitter source |
| 4 | APISIX upstreams had no `keepalive_pool` and no phase `timeouts` | Gateway-side handshake churn and unbounded upstream waits |
| 5 | Mobile `apiFetch` had no timeout or retry; `FlatList`s unwindowed | Spinners forever on dead connections; long lists jank on low-end devices |

The KPI aggregation path was already parallel (`WaitGroup` over sources) and
the Kafka consumers are single-row ops — no changes needed there.

## 2. What changed

### 2.1 Shared packages

**`packages/go-db`** — one pool factory and one server factory used by all 7
Go services:

- `dbpool.NewPool(ctx, dsn)` — `MaxConns` 32 (env `DB_MAX_CONNS`),
  `MinConns` 4 (env `DB_MIN_CONNS`, warm floor so bursts never pay the open
  cost), `MaxConnLifetime` 30 min, `MaxConnIdleTime` 10 min,
  `HealthCheckPeriod` 1 min (dead connections evicted before they cost a
  request).
- `dbpool.NewServer(addr, handler)` — `ReadHeaderTimeout` 10 s,
  `ReadTimeout` 15 s, `WriteTimeout` 60 s, `IdleTimeout` 90 s,
  `MaxHeaderBytes` 16 KiB.

**`packages/go-httpclient`** — `httpclient.New(timeout)` returns a client with
a tuned transport (`MaxIdleConns` 256, `MaxIdleConnsPerHost` 64,
`IdleConnTimeout` 90 s, dial timeout 2 s, keep-alive 30 s, HTTP/2 forced).
Wired into all 10 previously-default client sites: admin-api Keycloak admin
client, KPI toggle source, onboarding captcha verifier, ops health prober;
audit-log anomaly scorer, OpenSearch mirror, audit sink; citizen-api open-data
client; commerce-api Mojaloop client; infra-api webhook dispatcher.

### 2.2 Gateway (`infra/apisix/apisix.yaml`)

All 19 upstreams gained:

```yaml
keepalive_pool: { size: 512, requests: 1000, idle_timeout: 60 }
timeouts:       { connect: 2, send: 10, read: 30 }
```

`size` is sized for burst concurrency per upstream (gateway-wide idle cache);
`requests` rotates connections before NAT/firewall table pressure; `timeouts`
bounds connect storms and hung upstreams (slow-loris and leak protection) —
matching the service-side `http.Server` phase timeouts.

### 2.3 Mobile app (`apps/mobile`)

- `src/api/client.ts`: every request now runs under an `AbortController`
  deadline (default 10 s, `ApiError(0, …)` with a `timeout after Nms` message
  on expiry). Idempotent GETs retry up to 2 times with exponential backoff
  (300 ms, 900 ms) on transport error / 429 / 5xx. Mutations and 4xx responses
  never auto-retry — double-submit safety is preserved.
- react-query defaults already in place (`retry: 1`, `staleTime: 10 s`) are
  complementary: react-query dedupes/refetches, `apiFetch` owns transport
  behaviour. No double-retry amplification: react-query retries happen above a
  client that already backed off, and both are capped.
- All 5 `FlatList`s (Arrivals ×2, Alerts, Carbon, Driver jobs) gained
  windowing props: `initialNumToRender={12}`, `maxToRenderPerBatch={8}`,
  `updateCellsBatchingPeriod={50}`, `windowSize={9}`,
  `removeClippedSubviews` — first paint renders ~12 rows regardless of list
  length; per-scroll render work is bounded.

### 2.4 Benchmarks (`admin-api/internal/onboarding`)

`BenchmarkIntakeRequest` and `BenchmarkCitizenSelfServe` (httptest +
in-memory store/Keycloak doubles) bound the Go handler cost in isolation:

```
BenchmarkIntakeRequest-2        2000    ~82 µs/op    ~11.2 KB/op    67 allocs/op
BenchmarkCitizenSelfServe-2     2000    ~82 µs/op    ~11.7 KB/op    65 allocs/op
```

Handler-side cost is ~0.08 ms per request. The remaining budget to the p99
target is consumed by Postgres/Keycloak/network — exactly what the pool,
transport, and gateway keepalive tuning removes from the tail. (Benchmarks use
unique emails per iteration: the 5/email/24 h velocity cap and the
`(persona, lower(email))` unique index would otherwise 429/409 by design.)

## 3. Latency budget (per surface)

| Surface | p50 budget | p99 budget | Tuning that carries it |
|---------|-----------|-----------|------------------------|
| Gateway → service hop | < 5 ms | < 20 ms | APISIX keepalive_pool; service IdleTimeout 90 s |
| Service handler (Go) | < 10 ms | < 50 ms | warm pool floor; tuned transports (see §2.1) |
| Postgres round-trip | < 10 ms | < 30 ms | MaxConns 32, MinConns 4, health-checked pool |
| Keycloak admin call | < 30 ms | < 100 ms | pooled TLS (MaxIdleConnsPerHost 64), 10 s client timeout |
| End-to-end API (gateway in) | < 50 ms | < 300 ms | sum of the above; phase timeouts prevent tail blowups |
| Mobile first paint (list screen) | < 1.5 s | < 3 s | FlatList windowing; 10 s fetch deadline; backoff retry |

`WriteTimeout: 60 s` deliberately exceeds the p99 budget: it exists only to
free resources from hung writers, not to serve traffic.

## 4. Operating guidance

- Scale `DB_MAX_CONNS` with pod count × expected concurrent bursts
  (Postgres `max_connections` ÷ pods is the ceiling). `DB_MIN_CONNS=4` is a
  floor, not a target; raise it for always-hot services.
- If APISIX upstream count grows, keep `keepalive_pool.size` ≥ expected
  concurrent requests per upstream; watch `apisix_upstream_keepalive` metrics.
- Mobile timeout/backoff constants live in `src/api/client.ts` (`DEFAULT_TIMEOUT_MS`,
  `MAX_GET_RETRIES`, `BACKOFF_MS`) — tune per network profile, not per screen.
- Re-run benchmarks after any handler change:
  `go test -bench=. -benchtime=2000x ./internal/onboarding/`
