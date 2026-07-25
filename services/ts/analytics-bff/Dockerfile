# syntax=docker/dockerfile:1
# Build context must be the repo root so the @h2fleet/db file dependency
# resolves:
#   docker build -f services/ts/analytics-bff/Dockerfile .
FROM node:20-alpine AS build
WORKDIR /repo
COPY packages/db /repo/packages/db
COPY services/ts/analytics-bff /repo/services/ts/analytics-bff
# packages/db needs its own node_modules so the file: symlink target can
# resolve drizzle-orm/pg type declarations during typecheck/bundle.
RUN cd /repo/packages/db && npm ci --no-audit --no-fund
WORKDIR /repo/services/ts/analytics-bff
RUN npm ci --no-audit --no-fund \
 && npm run typecheck \
 && npm run build

FROM node:20-alpine
ENV NODE_ENV=production
WORKDIR /app
# Fully self-contained bundle (only optional pg-native is external).
COPY --from=build /repo/services/ts/analytics-bff/dist/server.cjs /app/server.cjs
USER node
EXPOSE 8096
CMD ["node", "/app/server.cjs"]
