# syntax=docker/dockerfile:1

# ---- build stage -------------------------------------------------------------
FROM node:20-alpine AS build
WORKDIR /repo

# The PWA consumes @h2fleet/toggle-client via a file: dependency, so the package
# source must be present at the same relative path as in the monorepo.
COPY packages/toggle-client/ts packages/toggle-client/ts
COPY apps/pwa apps/pwa

WORKDIR /repo/apps/pwa
# Reproducible install: package-lock.json is committed, so npm ci is the
# default; fall back to npm install if the lockfile is ever absent.
RUN if [ -f package-lock.json ]; then npm ci --no-audit --no-fund; else npm install --no-audit --no-fund; fi
RUN npm run build

# ---- serve stage -------------------------------------------------------------
FROM nginx:1.27-alpine
COPY apps/pwa/nginx.conf /etc/nginx/conf.d/default.conf
COPY --from=build /repo/apps/pwa/dist /usr/share/nginx/html

EXPOSE 80
HEALTHCHECK --interval=30s --timeout=3s --retries=3 \
  CMD wget -qO- http://127.0.0.1/ >/dev/null 2>&1 || exit 1
CMD ["nginx", "-g", "daemon off;"]
