# syntax=docker/dockerfile:1
# Build context must be the repo root so the toggle-client SDK and go-auth module are available:
#   docker build -f services/go/commerce-api/Dockerfile .
# NOTE: tigerbeetle-go bundles a native client library and requires CGO.
FROM golang:1.22-bookworm AS build
WORKDIR /src
COPY packages/toggle-client/go /packages/toggle-client/go
COPY packages/go-auth /packages/go-auth
COPY services/go/commerce-api /src
WORKDIR /src
RUN go mod download && CGO_ENABLED=1 GOOS=linux go build -ldflags="-s -w" -o /out/commerce-api ./cmd/server

FROM gcr.io/distroless/cc-debian12:nonroot
COPY --from=build /out/commerce-api /commerce-api
EXPOSE 8084
USER nonroot:nonroot
ENTRYPOINT ["/commerce-api"]
