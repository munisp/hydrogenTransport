# syntax=docker/dockerfile:1
# Build context must be the repo root so the toggle-client SDK and go-auth module are available:
#   docker build -f services/go/fleet-api/Dockerfile .
FROM golang:1.22-alpine AS build
WORKDIR /src
COPY packages/toggle-client/go /packages/toggle-client/go
COPY packages/go-auth /packages/go-auth
COPY services/go/fleet-api /src
WORKDIR /src
RUN go mod download && CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /out/fleet-api ./cmd/server

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/fleet-api /fleet-api
EXPOSE 8081
USER nonroot:nonroot
ENTRYPOINT ["/fleet-api"]
