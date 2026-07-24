module github.com/munisp/hydrogenTransport/services/go/commerce-api

go 1.22

require (
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/jackc/puddle/v2 v2.2.2 // indirect
	github.com/klauspost/compress v1.17.11 // indirect
	github.com/pierrec/lz4/v4 v4.1.22 // indirect
	github.com/twmb/franz-go/pkg/kmsg v1.9.0 // indirect
	go.uber.org/multierr v1.10.0 // indirect
	golang.org/x/crypto v0.32.0 // indirect
	golang.org/x/sync v0.10.0 // indirect
	golang.org/x/text v0.21.0 // indirect
)

require (
	github.com/go-chi/chi/v5 v5.1.0
	github.com/golang-jwt/jwt/v5 v5.2.1
	github.com/google/uuid v1.6.0
	github.com/jackc/pgx/v5 v5.7.2
	github.com/munisp/hydrogenTransport/packages/toggle-client/go v0.0.0
	github.com/tigerbeetle/tigerbeetle-go v0.16.4
	github.com/twmb/franz-go v1.18.1
	go.uber.org/zap v1.27.0
)

replace github.com/munisp/hydrogenTransport/packages/toggle-client/go => ../../../packages/toggle-client/go
