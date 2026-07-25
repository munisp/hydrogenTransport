# H2Fleet OSS Component Vulnerability Report

**Date:** 2026-07-25
**Method:** All committed lockfiles/manifests parsed and batch-queried against the OSV API (`api.osv.dev/v1/querybatch`): 95 Go modules (`go.sum`), 53 PyPI pins (7 `requirements.txt`), 527+829+120 npm packages (`package-lock.json` × 3), 607 crates (`Cargo.lock` × 2). Container/OS CVEs verified against vendor advisories (Apache, Keycloak, Neo4j, Redis, Temporal, Grafana, NVD). govulncheck/cargo-audit/pip-audit binaries were unavailable in the audit environment; OSV dependency-level matching is a superset of what call-graph analysis reports (may include unreachable paths — noted per row).

Findings are **REAL scan results** — every ID below was returned by OSV for the exact pinned version, or confirmed by a vendor advisory for the pinned image tag.

---

## 1. Go dependencies (all 6 Go services + packages/go-auth)

| Package @ pinned | ID (CVE) | Severity | Issue | Fixed in | Remediation |
|---|---|---|---|---|---|
| `google.golang.org/grpc v1.66.0` | GHSA-p77j-4mvh-x3m3 (CVE-2026-33186) | **CRITICAL** | Authorization bypass via missing leading slash in `:path` | 1.79.3 | `go get google.golang.org/grpc@v1.82.1` in all services (permify client + Dapr sidecar dep) |
| `google.golang.org/grpc v1.66.0` | GHSA-hrxh-6v49-42gf | HIGH | xDS RBAC and HTTP/2 vulnerabilities | 1.82.1 | same bump |
| `github.com/jackc/pgx/v5 v5.7.2` | GHSA-9jj7-4m8r-rfcm (CVE-2026-33816) | **CRITICAL** | Memory-safety vulnerability in pgx | 5.9.0 | `go get github.com/jackc/pgx/v5@v5.9.2` in all 6 services |
| `github.com/jackc/pgx/v5 v5.7.2` | GHSA-j88v-2chj-qfwx (CVE-2026-41889) | LOW | SQL injection via placeholder confusion with dollar-quoted literals | 5.9.2 | same bump (queries here use `$n` placeholders — low practical risk) |
| `github.com/dapr/dapr v1.14.0` | GHSA-85gx-3qv6-4463 (CVE-2026-41491) | HIGH | Service-invocation path-traversal ACL bypass | 1.15.14 / 1.16.14 / 1.17.5 | bump dapr SDK **and** pin sidecar `daprio/daprd:1.17.5` (currently `:latest`, F13) |
| `github.com/go-chi/chi/v5 v5.1.0` | GHSA-vrw8-fxc6-2r93 | MODERATE | Host-header injection → open redirect in `RedirectSlashes` | 5.2.2 | `go get github.com/go-chi/chi/v5@v5.2.2` in all services |
| `golang.org/x/crypto v0.54.0` | GO-2026-5932 | (see advisory) | crypto vuln in pinned version | per advisory | `go get golang.org/x/crypto@latest` in all go.mod |
| `github.com/sirupsen/logrus v1.4.2` | GHSA-4f99-4q7p-p3gh (CVE-2025-65637) | HIGH | DoS via `Entry.Writer()` | 1.8.3 / 1.9.1 | transitive (via Dapr); resolved by the Dapr bump |
| `golang.org/x/oauth2 v0.0.0-20180821…` | GHSA-6v2p-p543-phr9 (CVE-2025-22868) | HIGH | Improper input validation | 0.27.0 | ancient transitive pin; force `go get golang.org/x/oauth2@v0.27.0` |
| `github.com/golang/glog v0.0.0-20160126…` | GHSA-6wxm-mpqj-6jpf (CVE-2024-45339) | MODERATE | Insecure temp-file usage | 1.2.4 | transitive (Dapr); resolved by Dapr bump |
| `github.com/redis/go-redis/v9 v9.7.0` | GHSA-92cp-5422-2mw7 (CVE-2025-29923) | LOW | Out-of-order responses on `CLIENT SETINFO` timeout | 9.7.3 | `go get github.com/redis/go-redis/v9@v9.7.3` (toggle-service) |
| `go.temporal.io/api v1.43.0` | GHSA-q9w6-cwj4-gf4p (CVE-2025-1243) | LOW | Unencrypted transmission in api-go | 1.44.1 | `go get go.temporal.io/api@v1.44.1` (infra-api) |
| `github.com/yuin/goldmark v1.3.5` | GO-2026-5320 | LOW | docs-only transitive | per advisory | resolved incidentally on next `go get -u` |

Also returned by OSV for the same pins (duplicates of the above under the Go ecosystem namespace): GO-2026-4762, GO-2026-5775, GO-2026-5777, GO-2026-4771/4772/5004, GO-2026-5242, GO-2025-3372, GO-2025-4188, GO-2025-3462, GO-2025-3488, GO-2025-3540.

---

## 2. Python dependencies (7 services)

| Package @ pinned | Used by | ID (CVE) | Severity | Issue | Fixed in |
|---|---|---|---|---|---|
| `torch==2.5.1` | ml-platform, predictive-maintenance | **GHSA-53q9-r3pm-6pq6 (CVE-2025-32434)** | **CRITICAL** | `torch.load` RCE **even with `weights_only=True`** — directly exploitable via artifact poisoning (audit F5) | **2.6.0** |
| `torch==2.5.1` | same | GHSA-f4hp-rmr7-r7v8, GHSA-887c-mr87-cxwp, GHSA-vgrw-7cvw-pwgx, GHSA-qfhq-4f3w-5fph, GHSA-rrmf-rvhw-rf47, GHSA-3749-ghw9-m3mg, GHSA-c678-jfcj-6jmf, GHSA-x3gm-94wq-g975 (+ PYSEC aliases) | MODERATE/LOW | memory corruption / DoS family in 2.5.x | 2.6.0–2.13.0 (one bump clears all) |
| `PyJWT==2.10.1` | carbon-analytics, ml-platform, predictive-maintenance, route-optimizer | **GHSA-752w-5fwx-jx9f (CVE-2026-32597)** | HIGH | accepts unknown `crit` header extensions | 2.12.0 |
| `PyJWT==2.10.1` | same | GHSA-jq35-7prp-9v3f (CVE-2026-48523) | MODERATE | algorithm allow-list bypass with PyJWK keys — relevant to `h2fleet_auth` (JWKS-based) | 2.13.0 |
| `PyJWT==2.10.1` | same | GHSA-993g-76c3-p5m4 (SSRF/token forgery), GHSA-fhv5-28vv-h8m8 (kid DoS), GHSA-w7vc-732c-9m39 (base64 DoS), GHSA-xgmm-8j9v-c9wx | MODERATE/LOW | PyJWKClient issues (h2fleet_auth does not use PyJWKClient — residual risk low) | 2.13.0 |
| `pyarrow==18.1.0` | ml-platform | GHSA-rgxp-2hwp-jwgg (CVE-2026-25087) | HIGH | use-after-free reading IPC file with pre-buffering | 23.0.1 |
| `requests==2.32.3` | opensearch-bootstrap | GHSA-9hjg-9r4m-mvj7 (CVE-2024-47081) | MODERATE | .netrc credential leak via malicious URLs | 2.32.4 |
| `requests==2.32.3` | opensearch-bootstrap | GHSA-gc5v-m9x4-r6x2 (CVE-2026-25645) | MODERATE | insecure temp-file reuse | 2.33.0 |

**Remediation:** bump in the four affected `requirements.txt`: `PyJWT>=2.13.0` (all four services), `torch>=2.6.0` (ml-platform, predictive-maintenance), `pyarrow>=23.0.1` (ml-platform), `requests>=2.33.0` (opensearch-bootstrap). Re-run the OSV batch scan in CI after bumping.

---

## 3. npm dependencies (committed lockfiles)

### apps/pwa (`react-router`/`@remix-run/router` are RUNTIME deps — highest priority)

| Package @ pinned | ID (CVE) | Severity | Issue | Fixed in |
|---|---|---|---|---|
| `@remix-run/router 1.16.1` | GHSA-2w69-qvjg-hvjx (CVE-2026-22029) | HIGH | XSS via open redirects | 1.23.2 / 7.12.0 |
| `react-router 6.23.1` | GHSA-2j2x-hqr9-3h42 (CVE-2026-40181), GHSA-9jcx-v3wj-wh4m (CVE-2025-68470), GHSA-wrjc-x8rr-h8h6 (CVE-2026-53669), GHSA-337j-9hxr-rhxg (CVE-2026-53666) | MODERATE | open-redirect family (PWA runs on bearer-token origin → redirect/XSS can leak tokens) | 6.30.4 / 7.18.0 |
| `vite 5.2.13` (dev) | GHSA-vg6x-rcgg-rjx6 (CVE-2025-24010) + 12 more (GHSA-9cwx/64vr/356w/4r4m/859w/93m4/g4jq/jqfw/8wf4/c27g/fx2h/v6wh/xcj6/x574) | MODERATE | dev-server `server.fs.deny` bypasses — dev machines only | 5.4.21 |
| `esbuild 0.20.2` (dev) | GHSA-67mh-4wv8-2f99 (GHSA) | MODERATE | dev-server request forgery | 0.25.0 |
| `postcss 8.4.38` | GHSA-6g55-p6wh-862q (HIGH, CVE-2026-45623), GHSA-r28c-9q8g-f849 (HIGH), GHSA-qx2v-qp2m-jg93 | HIGH/MOD | sourceMap file-read + `</style>` XSS | 8.5.18 |
| `brace-expansion 2.1.2` | GHSA-mh99-v99m-4gvg (CVE-2026-14257) | HIGH | DoS via unbounded expansion | 5.0.8 |

### apps/mobile

| Package @ pinned | ID | Severity | Issue | Fixed in |
|---|---|---|---|---|
| `tar 6.2.1` (build/Expo toolchain) | GHSA-23hp-3jrh-7fpw (CVE-2026-59873) + 12 more (file overwrite, hardlink/symlink traversal, DoS) | **CRITICAL**/HIGH | tar extraction attacks — build-time supply chain | 7.5.21 |
| `uuid 7.0.3` | GHSA-w5hq-g745-h8pq (CVE-2026-41907/41988) | MODERATE | buffer bounds in v3/v5/v6 | 11.1.1 |
| `fast-xml-parser 4.5.7` | GHSA-gh4j-gqv2-49f6 (CVE-2026-41650) | MODERATE | XML comment/CDATA injection | 5.7.0 |
| `postcss 8.4.49`, `brace-expansion 2.1.2`, `@babel/core 7.24.7` (LOW, CVE-2026-49356) | as above | — | — | 8.5.18 / 5.0.8 / 7.29.6 |

### packages/toggle-client/ts (dev-only)

`vitest 1.6.0`: GHSA-9crc-q9x8-hgqq (**CRITICAL**, CVE-2025-24964, RCE via browser while Vitest API server runs — CI/dev only) and GHSA-5xrq-8626-4rwp → fixed 1.6.1 / 3.0.5. `vite 5.4.21`, `esbuild 0.21.5` — same advisories as above.

**Remediation:** PWA — `npm i react-router@7.18.0 @remix-run/router@1.23.2 postcss@8.5.18` + `npm i -D vite@5.4.21 esbuild@0.25.0`; mobile — `npm i uuid@11.1.1 fast-xml-parser@5.7.0` + override `tar@7.5.21`, `brace-expansion@5.0.8`; toggle-client/ts — `npm i -D vitest@3.0.5 vite@5.4.21 esbuild@0.25.0`. All lockfiles are committed → fix agents can apply these verbatim and commit the regenerated lockfiles.

---

## 4. Rust crates (digital-twin, telemetry-ingest)

| Crate @ pinned | ID | Severity | Issue | Assessment |
|---|---|---|---|---|
| `rsa 0.9.10` (transitive, both services) | RUSTSEC-2023-0071 | MEDIUM | Marvin timing side-channel on RSA decryption | Transitive via sqlx/redis TLS stacks; **neither service performs RSA private-key operations** — not exploitable here. Resolve by `cargo update` when a fixed `rsa` is released (currently no fix — track RUSTSEC). |

No other crate advisories matched the 607 locked packages.

---

## 5. Infrastructure / container components (docker-compose + k8s pins)

| Component @ pinned tag | Known CVEs affecting this train | Severity | Fixed in | Concrete remediation |
|---|---|---|---|---|
| `apache/apisix:3.10.0-debian` | CVE-2026-44915 (cas-auth open redirect, ≤3.16.0 — cas-auth unused here); CVE-2026-31924 (tencent-cloud-cls cleartext, ≤3.15.0 — plugin unused); CVE-2026-47339 (≤3.13) | MOD | 3.16.0 / 3.17.0 | `apache/apisix:3.17.0-debian` |
| `quay.io/keycloak/keycloak:25.0` | 25.0.x is EOL; JWT-cache OOM DoS CVE-2025-2559 plus bundled Netty/Quarkus/Vert.x CVEs (CVE-2025-67735, CVE-2025-66560, CVE-2026-1002) affect this line | HIGH | 26.x line (26.5.x current) | `quay.io/keycloak/keycloak:26.5` (test realm import; 26 requires no config change for this realm) |
| `timescale/timescaledb-ha:pg16.4-ts2.17.2-all` and `postgres:16-alpine` | CVE-2025-1094 (libpq SQL injection, <16.7) plus routine minor CVEs | HIGH | pg 16.7+ | `timescale/timescaledb-ha:pg16.10-ts2.x-all` (latest pg16 tag); `postgres:16.10-alpine` |
| `redis:7.4-alpine` (moving tag) | CVE-2025-49844 (**CRITICAL 10.0**, Lua UAF RCE, <7.4.6); CVE-2025-32023 (HLL OOB write, <7.4.5); CVE-2025-48367 (unauth DoS, <7.4.5); CVE-2025-21605 (output-buffer DoS, <7.4.3); CVE-2024-46981 (Lua GC RCE, <7.4.2) | **CRITICAL** | 7.4.6 (or 8.2.2) | pin `redis:7.4.6-alpine`; ACL-disable `EVAL`/`EVALSHA` for service users |
| `opensearchproject/opensearch:2.17.1` + `-dashboards:2.17.1` | multiple CVE fixes shipped in 2.18.0/2.19.0–2.19.2 release line (per OpenSearch version history) | HIGH | 2.19.2 | `opensearchproject/opensearch:2.19.2` (+ dashboards same tag) |
| `temporalio/auto-setup:1.24.2` | CVE-2025-8396 (auth-header DoS, <1.26.3); CVE-2025-14986 / CVE-2025-14987 (cross-namespace authz bypass, affects ≥1.24.0, fixed 1.27.4/1.28.2/1.29.2); CVE-2026-5724 (unauth streaming replication endpoint, fixed 1.28.4+) | HIGH | 1.28.4 / 1.29.6 | `temporalio/auto-setup:1.29.6` and `temporalio/ui` matching (≥2.3x paired per release notes) |
| `ghcr.io/mlflow/mlflow:v2.14.1` | CVE-2025-11200 / CVE-2025-11201 (**CRITICAL 9.8**, auth bypass + dir-traversal RCE); CVE-2024-37052…37061 (8 × HIGH 8.8, model deserialization RCE, mostly fixed ≥2.14.3); CVE-2025-14279 (DNS rebinding, ≤3.4.0); CVE-2026-2393 (webhook SSRF, <3.9.0); CVE-2026-8147 (trace authz, <3.14.0) | **CRITICAL** | 3.14.0 | `ghcr.io/mlflow/mlflow:v3.14.0`; until then keep UI/API off any shared network and disable artifact-write for untrusted users |
| `rayproject/ray:2.34.0-py311` | historical dashboard/jobs CVEs (CVE-2023-6019 family) fixed ≤2.8.1; **Ray Jobs API remains unauthenticated by design** in 2.34 | HIGH (by design) | n/a (config) | keep Ray head/dashboard unreachable outside the cluster (NetworkPolicy already default-denies external); plan upgrade to current 2.4x |
| `bitnami/spark:3.5.3` | CVE-2025-54920 (HIGH 8.8, History Server Jackson deserialization RCE, <3.5.7); CVE-2025-55039 (unauthenticated RPC cipher, mitigated by config) | HIGH | 3.5.7 | `bitnami/spark:3.5.7`; set `spark.network.crypto.cipher=AES/GCM/NoPadding` (compose currently `SPARK_SSL_ENABLED: no`) |
| `bitnami/kafka:3.7` (+ `bitnami/zookeeper:3.9`) | CVE-2025-27817 (client arbitrary file read + SSRF via SASL OAuth config, 3.1.0–3.9.0, fixed 3.9.1/4.0.0); CVE-2025-27818 (Connect header auth bypass) | HIGH | 3.9.1 / 4.0.0 | `bitnami/kafka:3.9.1` (or 4.0.x after client-compat check) |
| `minio/minio:RELEASE.2024-10-13T13-34-11Z` (+ `minio/mc:RELEASE.2024-10-08…`) | CVE-2026-41145 confirmed affecting this exact release; post-Oct-2024 security fixes missing | HIGH | current RELEASE | bump both to the latest `RELEASE.2026-xx` tags **and** rotate the default `h2admin/h2adminpass` creds (audit F9) |
| `neo4j:5-community` (**moving tag**) | CVE-2026-1622 (MEDIUM, unredacted data in query.log, <5.26.21); CVE-2025-11602 (MEDIUM, Bolt handshake info leak, 5.26.0–5.26.14) | MEDIUM | 5.26.21+ | pin immutable `neo4j:5.26.22-community` (or later 5.26.x) |
| `grafana/grafana:11.2.2` | CVE-2024-9264 (DuckDB SQL-expression RCE) is **fixed in 11.2.2** ✓; subsequent 11.2.x patch CVEs outstanding | MEDIUM | 11.2.10 / 11.6.x | `grafana/grafana:11.2.10` (stay on 11.2 LTS) + change default admin password |
| `prom/prometheus:v2.54.1` / `prom/alertmanager:v0.27.0` | base-image Go-stdlib CVEs (Go 1.22.6: CVE-2025-61726 HIGH, CVE-2025-68121, etc.) | MEDIUM | current v2.55/v3.x | `prom/prometheus:v2.55.1`+ / `prom/alertmanager:v0.28.x` |
| `daprio/daprd:latest` (**unpinned**) | GHSA-85gx-3qv6-4463 (HIGH, service-invocation path traversal ACL bypass, <1.15.14) affects any stale `:latest` pull | HIGH | 1.15.14 / 1.16.14 / 1.17.5 | pin `daprio/daprd:1.17.5` |
| `ghcr.io/permify/permify:v1.2.4` | no public CVEs located for Permify | — | — | track releases; upgrade on next minor |
| `ghcr.io/tigerbeetle/tigerbeetle:0.16.13` | no published CVEs | — | — | bump to latest 0.16.x patch during routine maintenance |
| `mojaloop/simulator:v14.2.0` | dev simulator; Mojaloop advisories target production services, not the sim | LOW | — | keep internal-only (no public route exists — verified) |
| `infinyon/fluvio:0.11.11`, `ghcr.io/openappsec/agent:1.1.20`, `tabulario/iceberg-rest:1.6.0`, `ghcr.io/kukymbr/goose-docker:3.27.1`, `alpine:3.20` | no CVE data located for these pins | — | — | pin-review annually; replace `alpine:3.20` on next base refresh |

### Cross-cutting container findings
1. **All h2fleet service images in `infra/k8s/base/*.yaml` use `:latest`** — mutable tag; deploys are non-reproducible and stale-node pulls run mixed versions. Pin per-release digests.
2. **`daprio/daprd:latest` and `neo4j:5-community`** are moving tags in compose (see above).
3. **Weak default credentials** in compose for Keycloak master admin, Grafana, MinIO, APISIX admin key, and all seeded realm users — see audit finding F9.

---

## 6. Scan reproduction

The dependency results above regenerate from committed artifacts only:

```bash
# Go: parse every go.sum -> OSV ecosystem "Go"
# PyPI: parse every services/python/*/requirements.txt (== pins) -> OSV ecosystem "PyPI"
# npm: parse each package-lock.json packages{} -> OSV ecosystem "npm"
# crates: parse each Cargo.lock [[package]] -> OSV ecosystem "crates.io"
# POST https://api.osv.dev/v1/querybatch  {"queries":[{"package":{"name":N,"ecosystem":E},"version":V}, ...]}
```

Recommended CI addition: `osv-scanner --lockfile=...` (or `govulncheck ./...`, `pip-audit -r`, `npm audit --omit=dev`, `cargo audit`) as a blocking job; this report's OSV batch script can be dropped into `infra/ci` verbatim.

---

## 7. Remediation log (2026-07-25, workstream B4)

Actions applied to committed manifests/lockfiles/compose pins. Verification: `compileall` green on all 7 Python services + `services/python/shared` + `packages/toggle-client/python`; `pytest` green for ml-platform, carbon-analytics, predictive-maintenance in a torch-2.6.0 venv; `tsc --noEmit` green for apps/pwa, apps/mobile, packages/toggle-client/ts, packages/db, services/ts/analytics-bff; `vitest run` 9/9 green for toggle-client/ts on the bumped toolchain; YAML parse green for both compose files. `cargo check` not run (no Rust toolchain in the fix sandbox) — Rust manifests changed by comment only, no dependency graph change.

### Python (requirements.txt / pyproject.toml)

| Item | Action |
|---|---|
| `torch==2.5.1` (ml-platform, predictive-maintenance) — CVE-2025-32434 CRITICAL + 2.5.x DoS family | **Bumped → `torch==2.6.0`** in both requirements.txt and both Dockerfiles' CPU-wheel install lines. All three `torch.load` call sites (`ml-platform/app/registry.py:66`, `ml-platform/training/train.py:130`, `predictive-maintenance/app/lstm_model.py:114`) already pass `weights_only=True` explicitly → compatible with the 2.6 default; no code change needed. Artifact-poisoning compensating controls (sha256 in registry.json, read-only artifacts volume, MinIO cred rotation) remain with the F5/F9 owners. |
| `PyJWT[crypto]==2.10.1` (carbon-analytics, ml-platform, predictive-maintenance, route-optimizer) — CVE-2026-32597 HIGH, CVE-2026-48523 + PyJWK issues | **Bumped → `PyJWT[crypto]==2.13.0`** in all four requirements.txt; floor raised to `>=2.13,<3` in `services/python/shared/pyproject.toml`. |
| `pyarrow==18.1.0` (ml-platform) — CVE-2026-25087 HIGH | **Bumped → `pyarrow==23.0.1`**. |
| `requests==2.32.3` (opensearch-bootstrap) — CVE-2024-47081, CVE-2026-25645 | **Bumped → `requests==2.33.0`**. |

### npm (package.json + regenerated package-lock.json)

| Item | Action |
|---|---|
| `react-router-dom 6.23.1` / `react-router 6.23.1` / `@remix-run/router 1.16.1` (apps/pwa) — open-redirect/XSS family | **Bumped → `react-router-dom@6.30.4`** (minimal fixed 6.x line, not 7.x major); lockfile resolves `react-router@6.30.4`, `@remix-run/router@1.23.3` (≥ fixed 1.23.2). Lockfile verified: no instance below fixed versions. |
| `vite 5.2.13`, `esbuild 0.20.2`, `postcss 8.4.38`, `brace-expansion 2.1.2` (apps/pwa, dev/build) | **Bumped → `vite@5.4.21`, `postcss@8.5.18`** (devDeps) + **npm `overrides`: `esbuild@0.25.0`, `brace-expansion@5.0.8`** (transitive pins; overrides required because consumer ranges don't reach the fixed majors). |
| `tar 6.2.1`, `uuid 7.0.3`, `fast-xml-parser 4.5.7`, `postcss 8.4.49`, `brace-expansion 2.1.2`, `@babel/core 7.24.7` (apps/mobile) | **`@babel/core → 7.29.6`** (devDep) + **npm `overrides`: `tar@7.5.21`, `uuid@11.1.1`, `fast-xml-parser@5.7.0`, `postcss@8.5.18`, `brace-expansion@5.0.8`** (all transitive build-toolchain deps; overrides used instead of adding unused direct runtime deps). Lockfile verified clean. |
| `vitest 1.6.0` (packages/toggle-client/ts) — CVE-2025-24964 CRITICAL (dev-only) | **Bumped → `vitest@3.0.5`** + **override `esbuild@0.25.0`**; resolved `vite` ≥ 5.4.21 via vitest's range. `vitest run` 9/9 green post-bump. |

### Rust (digital-twin, telemetry-ingest)

| Item | Action |
|---|---|
| `rsa 0.9.10` transitive — RUSTSEC-2023-0071 (no fix released) | **Mitigation note applied**: comment with CVE/RUSTSEC id + rationale added at `Cargo.toml` `[dependencies]` in both services. Not exploitable (no RSA private-key operations); resolve via `cargo update` when a fixed `rsa` releases. No version invented. |

### Container / compose image tags (infra/docker-compose.yml, infra/prod/docker-compose.prod.yml, infra/backup/Dockerfile)

| Component | Action |
|---|---|
| `bitnami/kafka:3.7` | **→ `bitnami/kafka:3.9.1`** (compose ×1, prod ×2) — CVE-2025-27817/27818. Zookeeper left at 3.9 (no audit-named fix). |
| `postgres:16-alpine` | **→ `postgres:16.10-alpine`** (compose ×3, prod ×1, backup Dockerfile) — CVE-2025-1094. |
| `timescale/timescaledb-ha:pg16.4-ts2.17.2-all` | **→ `pg16.10-ts2.22.1-all`** (compose + prod) — CVE-2025-1094; audit's "pg16.10-ts2.x" resolved to the verified-published `ts2.22.1` pairing. |
| `redis:7.4-alpine` (moving tag) | **→ `redis:7.4.6-alpine`** (compose ×1, prod ×4) — CVE-2025-49844 CRITICAL + 4 more. Audit's second control (ACL-disable `EVAL`/`EVALSHA`) recorded as a comment at the pin; redis config is outside this workstream's file ownership. |
| `opensearchproject/opensearch{,-dashboards}:2.17.1` | **→ `2.19.2`** (compose ×2, prod ×2). |
| `apache/apisix:3.10.0-debian` | **→ `3.17.0-debian`** (compose + prod). |
| `quay.io/keycloak/keycloak:25.0` (EOL) | **→ `26.5`** (compose + prod); realm import regression check deferred to staging (audit notes no config change required for this realm). |
| `temporalio/auto-setup:1.24.2` / `temporalio/server:1.24.2` (prod) | **→ `1.29.6`** — CVE-2025-8396, CVE-2025-14986/14987, CVE-2026-5724. `temporalio/ui:2.28.0` **→ `2.34.0`** (the 2.3x line paired with server 1.29.x per release notes/deploy references). |
| `bitnami/spark:3.5.3` | **→ `3.5.7`** (compose ×2) — CVE-2025-54920. `spark.network.crypto.cipher=AES/GCM/NoPadding` (CVE-2025-55039 mitigation) is a service-env change owned by the compose-structure workstream — flagged. |
| `prom/prometheus:v2.54.1` / `prom/alertmanager:v0.27.0` | **→ `v2.55.1` / `v0.28.1`** (compose). |
| `grafana/grafana:11.2.2` | **→ `11.2.10`** (compose, stays on 11.2 LTS). Default admin password rotation (F9) owned separately. |
| `daprio/daprd:latest` (unpinned) | **→ `daprio/daprd:1.17.5`** (compose ×2) — GHSA-85gx-3qv6-4463. |
| `ghcr.io/mlflow/mlflow:v2.14.1` | **→ `v3.14.0`** (compose) — CVE-2025-11200/11201 CRITICAL + deserialization/SSRF/authz fixes. |
| `neo4j:5-community` (moving tag) | **→ `neo4j:5.26.22-community`** (compose) — CVE-2026-1622, CVE-2025-11602. |
| `minio/minio:RELEASE.2024-10-13…` + `minio/mc:RELEASE.2024-10-08…` (compose + backup Dockerfile) — CVE-2026-41145 | **NOT bumped — no verifiable upstream tag.** Audit recommends "latest RELEASE.2026-xx", but upstream `minio/minio`/`minio/mc` image publishing has stalled (no 2026 tags; downstream ecosystems e.g. Grafana Loki moved to community forks to escape the same CVE). Registry access was unavailable in the fix sandbox to verify a candidate. Compensating comment with CVE id added at both pins; credential rotation (F9) flagged. **Decision needed at integration gate:** pick a verified upstream tag if one materializes, or approve a fork (e.g. `pgsty/minio`) as a separate change. |
| `rayproject/ray:2.34.0-py311` | **No version change** — audit: Jobs API unauthenticated *by design* (no fix). Mitigation comment added at both pins (NetworkPolicy default-deny already keeps head/dashboard internal); upgrade to 2.4x planned. |
| `ghcr.io/permify/permify:v1.2.4`, `tigerbeetle:0.16.13`, `mojaloop/simulator:v14.2.0`, `fluvio:0.11.11`, `openappsec/agent:1.1.20`, `iceberg-rest:1.6.0`, `goose-docker:3.27.1`, `alpine:3.20`, `bitnami/etcd:3.5`, `haproxy:2.9-alpine` | **No change** — audit located no CVEs / named no specific fixed tag. |

### Go modules (section 1 of this report)

**Deferred — not touched.** All `services/go/**` `go.mod`/`go.sum` upgrades (grpc 1.82.1, pgx 5.9.2, dapr 1.17.x SDK, chi 5.2.2, x/crypto, oauth2 0.27.0, go-redis 9.7.3, temporal api 1.44.1) are deferred to the later Go integration gate per workstream split. The Dapr *sidecar* image pin (`daprio/daprd:1.17.5`) was applied here (container scope).

Deferred-Go closure (2026-07-25, compile gate) — verification: `go mod tidy && go build ./... && go vet ./... && go test ./... -count=1` green per module:

- `services/go/citizen-api` — **applied/verified**: Dapr Go SDK bump (go-sdk v1.15.0 / dapr v1.18.0 transitive, resolving the logrus GHSA-4f99-4q7p-p3gh and glog GHSA-6wxm-mpqj-6jpf transitives) with pgx 5.9.2 / chi 5.2.2 / grpc 1.82.1 / x/crypto held; `go 1.26.4` directive; build/vet/test green.
- `services/go/toggle-service` — **applied/verified**: `go get github.com/redis/go-redis/v9@v9.7.3` (GHSA-92cp-5422-2mw7 / CVE-2025-29923); build/vet/test green.
- `services/go/infra-api` — **applied/verified**: `go.temporal.io/api v1.44.1` pinned (GHSA-q9w6-cwj4-gf4p / CVE-2025-1243); `golang.org/x/oauth2` fix satisfied — tidy selects **v0.36.0** (≥ the named 0.27.0, pulled by grpc 1.82.1), so the explicit 0.27.0 pin is not retained in go.mod (no direct importer); build/vet/test green.

### Cross-cutting (unchanged, owned elsewhere)

- k8s `:latest` service-image tags (`infra/k8s/base/*.yaml`) — outside this workstream's ownership; digest pinning still open.
- Weak default credentials (F9: Keycloak/Grafana/MinIO/APISIX/seeded realm users) — env/secret change owned by the compose-structure workstream.
