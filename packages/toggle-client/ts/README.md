# @h2fleet/toggle-client

TypeScript SDK for the H2Fleet Feature Toggle Service. Same contract as the Go and Python
SDKs (SPEC §3.2):

- `isEnabled(module): Promise<boolean>` — checks `GET {baseUrl}/v1/toggles/{module}`
- `getAll(): Promise<Record<string, boolean>>` — reads `GET {baseUrl}/v1/toggles`
- 5 second local cache (configurable), in-flight request deduplication
- **Fail-closed**: any error (network, timeout, non-2xx, malformed body) resolves to
  `false` / `{}` — a module is only ever considered enabled on an explicit `enabled: true`

## Usage

```ts
import { ToggleClient } from "@h2fleet/toggle-client";

// In-cluster: http://toggle-service:8080 — via APISIX: https://gateway/api/toggles
const toggles = new ToggleClient("http://localhost:9080/api/toggles");

if (await toggles.isEnabled("telematics")) {
  // start telemetry consumers, show nav entry, ...
}
```

`MODULE_IDS` exports the 20 canonical module identifiers from SPEC §3.1.

## Consuming from the PWA

The PWA depends on this package with a `file:` dependency
(`"@h2fleet/toggle-client": "file:../../packages/toggle-client/ts"`). The package's
entry point is the TypeScript source itself (`src/index.ts`); Vite/esbuild compiles it
as part of the app build, so no separate build step is required in the monorepo.

## Development

```bash
npm install
npm run typecheck   # tsc --noEmit
npm test            # vitest
```
