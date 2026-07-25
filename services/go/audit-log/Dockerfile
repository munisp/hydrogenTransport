# syntax=docker/dockerfile:1
# Build context must be the repo root so the go-auth module is available:
#   docker build -f services/go/audit-log/Dockerfile .
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY packages/go-auth /packages/go-auth
COPY services/go/audit-log /src
WORKDIR /src
RUN go mod download && CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /out/audit-log ./cmd/server

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/audit-log /audit-log
EXPOSE 8086
USER nonroot:nonroot
ENTRYPOINT ["/audit-log"]
