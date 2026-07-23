# H2Fleet Mobile (`apps/mobile`)

Expo React Native (SDK 51) app covering the `mobile-app` module (SPEC §3.1):

- **Citizen tabs** — live arrivals board, DRT on-demand shuttle request, service alerts,
  carbon impact stats.
- **Driver tab** — assigned jobs from dispatch (accept in place) and a modal incident
  report button posting straight into the safety workflow.

## Configuration

All runtime config lives in `app.json → expo.extra` (single source, no `.env` needed):

```jsonc
{
  "extra": {
    "apiBase": "http://localhost:9080",   // APISIX gateway (SPEC §3.6)
    "keycloak": { "url": "...", "realm": "h2fleet", "clientId": "h2fleet-mobile" },
    "arrivalsPollMs": 20000,
    "togglesPollMs": 30000
  }
}
```

`src/config.ts` reads it via `expo-constants`. For a physical device on LAN, point
`apiBase` at the machine running APISIX (e.g. `http://192.168.1.10:9080`).

## Feature toggles

Tabs are gated by the same contract as the PWA (SPEC §3.2): `GET /api/toggles/v1/toggles`
polled every 30s. `passenger-pwa` controls Arrivals/Alerts, `demand-responsive` the DRT
tab, `carbon-credits` the Carbon tab, `mobile-app` the Driver tab. The client fails
closed — unreachable toggle service hides gated tabs once the first fetch fails.

## API usage

`src/api/client.ts` mirrors the PWA contracts (snake_case fields per SPEC §3.4):

| Surface | Endpoint |
|---|---|
| Arrivals | `GET /api/citizen/v1/stops`, `GET /api/citizen/v1/arrivals?stop_id=` |
| DRT | `POST /api/citizen/v1/drt/requests`, `GET /api/citizen/v1/drt/requests` |
| Alerts | `GET /api/citizen/v1/alerts` |
| Carbon | `GET /api/citizen/v1/carbon/credits` |
| Driver jobs | `GET /api/infra/v1/dispatch/jobs`, `POST .../jobs/{id}/accept` |
| Incident report | `POST /api/infra/v1/incidents` |

## Run

```bash
npm install
npm start          # Expo dev server; scan with Expo Go or press a/i for emulators
npm run typecheck  # tsc --noEmit
```

OIDC login (Keycloak) is stubbed behind `setAccessToken()` in the API client; the token
is attached to every request once set. Wiring the full `expo-auth-session` PKCE flow is
the next iteration — the config block above already carries the client settings.

Design follows the platform palette: warm stone background, amber `#b45309` accents,
teal `#0f766e` for energy/eco signals.
