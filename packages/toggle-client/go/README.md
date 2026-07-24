# toggle-client (Go)

Go SDK for the H2Fleet feature-toggle service (SPEC §3.2). Semantics are identical
to the TS and Python SDKs in this monorepo:

- `isEnabled(module) -> bool`
- 5s local cache per module (negative results are cached too)
- **fail-closed**: any error (network, non-200, malformed JSON) returns `false`

## Usage

```go
import toggle "github.com/munisp/hydrogenTransport/packages/toggle-client/go"

tc := toggle.New(os.Getenv("TOGGLE_URL")) // e.g. http://toggle-service:8080
if !tc.IsEnabled(ctx, "digital-twin") {
    // module disabled: domain routes return 404, consumers idle
}
```

`Invalidate(module)` drops a cached entry — call it when consuming a
`toggle.changed` Kafka event to pick up changes faster than the 5s TTL.

## Test

```sh
go test ./...
```
