# syntax=docker/dockerfile:1
# Build context must be the repo root (consistent with the other Go services):
#   docker build -f services/go/toggle-service/Dockerfile .
FROM golang:1.22-alpine AS build
WORKDIR /src
COPY services/go/toggle-service/go.mod services/go/toggle-service/go.sum ./
RUN go mod download
COPY services/go/toggle-service ./
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /out/toggle-service ./cmd/server

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/toggle-service /toggle-service
EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/toggle-service"]
