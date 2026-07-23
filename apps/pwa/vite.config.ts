import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import { VitePWA } from "vite-plugin-pwa";

// APISIX gateway. In dev, all /api/* traffic is proxied here (SPEC §3.6).
const APISIX_URL = process.env.APISIX_URL ?? "http://localhost:9080";

export default defineConfig({
  plugins: [
    react(),
    VitePWA({
      registerType: "autoUpdate",
      includeAssets: ["icons/icon.svg"],
      manifest: {
        name: "H2Fleet",
        short_name: "H2Fleet",
        description: "Unified hydrogen bus fleet platform — fleet, infrastructure, citizen and commerce operations.",
        theme_color: "#44403c",
        background_color: "#fafaf9",
        display: "standalone",
        start_url: "/",
        scope: "/",
        icons: [
          { src: "icons/icon.svg", sizes: "any", type: "image/svg+xml", purpose: "any" },
          { src: "icons/icon.svg", sizes: "any", type: "image/svg+xml", purpose: "maskable" },
        ],
      },
      workbox: {
        // Offline shell: precache the app shell, never cache API or auth traffic.
        navigateFallback: "index.html",
        navigateFallbackDenylist: [/^\/api\//, /^\/silent-check-sso\.html/],
        runtimeCaching: [
          {
            urlPattern: ({ url }) => url.pathname.startsWith("/api/"),
            handler: "NetworkOnly",
          },
          {
            urlPattern: ({ url }) => url.hostname.includes("maplibre") || url.hostname.includes("tile"),
            handler: "CacheFirst",
            options: {
              cacheName: "h2fleet-map-tiles",
              expiration: { maxEntries: 300, maxAgeSeconds: 7 * 24 * 3600 },
            },
          },
        ],
      },
    }),
  ],
  server: {
    port: 5173,
    proxy: {
      "/api": {
        target: APISIX_URL,
        changeOrigin: true,
      },
    },
  },
  build: {
    target: "es2020",
    sourcemap: true,
  },
});
