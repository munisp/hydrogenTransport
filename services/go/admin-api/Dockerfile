# syntax=docker/dockerfile:1
# Build context must be the repo root so the go-auth module is available:
#   docker build -f services/go/admin-api/Dockerfile .
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY packages/go-auth /packages/go-auth
COPY services/go/audit-log /services/go/audit-log
COPY services/go/admin-api /src
WORKDIR /src
RUN go mod download && CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /out/admin-api ./cmd/server

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/admin-api /admin-api
EXPOSE 8085
USER nonroot:nonroot
ENTRYPOINT ["/admin-api"]
