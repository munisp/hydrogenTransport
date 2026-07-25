/// <reference types="vite-plugin-pwa/client" />
// PWA service-worker registration + update lifecycle (deployment correctness,
// paired with apps/pwa/nginx.conf cache busting).
//
// Flow when a new bundle is deployed:
//   1. Browser re-fetches /sw.js (never cached) and detects the new worker.
//   2. onNeedRefresh fires -> we show a one-time "new version" toast.
//   3. updateSW(true) tells the waiting worker to skipWaiting + clients.claim()
//      (workbox handles both inside the SW on message).
//   4. controllerchange -> purge stale caches, then reload exactly once so the
//      page picks up the new hashed assets referenced by the new index.html.
//
// Workbox's cleanupOutdatedCaches already prunes outdated precaches inside
// the SW during activate; purgeStaleCaches() additionally removes any legacy
// h2fleet-* runtime caches from older builds (the map-tile cache is kept).

const KEEP_CACHES = new Set(["h2fleet-map-tiles"]);
const STALE_PREFIXES = ["h2fleet-", "workbox-"];

// purgeStaleCaches deletes old caches left by previous SW versions.
export async function purgeStaleCaches(): Promise<string[]> {
  if (typeof caches === "undefined") return [];
  const keys = await caches.keys();
  const stale = keys.filter(
    (k) => STALE_PREFIXES.some((p) => k.startsWith(p)) && !KEEP_CACHES.has(k),
  );
  await Promise.all(stale.map((k) => caches.delete(k)));
  return stale;
}

// showUpdateToast renders a minimal one-time toast (no framework dependency)
// with a Reload action. Shown at most once per deployed version.
function showUpdateToast(version: string, onReload: () => void): void {
  const marker = `h2fleet-update-toast-${version}`;
  if (sessionStorage.getItem(marker) || document.getElementById("h2fleet-update-toast")) {
    return;
  }
  sessionStorage.setItem(marker, "1");
  const el = document.createElement("div");
  el.id = "h2fleet-update-toast";
  el.setAttribute("role", "status");
  el.style.cssText =
    "position:fixed;bottom:1rem;left:50%;transform:translateX(-50%);z-index:9999;" +
    "background:#1c1917;color:#fafaf9;padding:.75rem 1rem;border-radius:.5rem;" +
    "display:flex;gap:.75rem;align-items:center;font:500 14px/1.4 system-ui,sans-serif;" +
    "box-shadow:0 4px 16px rgba(0,0,0,.35)";
  const text = document.createElement("span");
  text.textContent = "A new version of H2Fleet is available.";
  const btn = document.createElement("button");
  btn.type = "button";
  btn.textContent = "Reload";
  btn.style.cssText =
    "background:#0ea5e9;border:none;color:#fff;padding:.4rem .8rem;" +
    "border-radius:.375rem;cursor:pointer;font:inherit";
  btn.addEventListener("click", () => {
    el.remove();
    onReload();
  });
  el.append(text, btn);
  document.body.appendChild(el);
}

// registerServiceWorker wires vite-plugin-pwa's registerSW with the update
// lifecycle above. No-op in dev and when SWs are unsupported.
export async function registerServiceWorker(): Promise<void> {
  if (!import.meta.env.PROD || typeof navigator === "undefined" || !("serviceWorker" in navigator)) {
    return;
  }
  const version =
    (document.querySelector('meta[name="h2fleet-version"]') as HTMLMetaElement | null)?.content ??
    "unknown";

  let reloading = false;
  navigator.serviceWorker.addEventListener("controllerchange", () => {
    // New worker took control: drop caches from older builds, then reload
    // once so the page runs the new bundle against the fresh index.html.
    if (reloading) return;
    reloading = true;
    void purgeStaleCaches().finally(() => window.location.reload());
  });

  try {
    const { registerSW } = await import("virtual:pwa-register");
    const updateSW = registerSW({
      immediate: true,
      onNeedRefresh() {
        showUpdateToast(version, () => void updateSW(true));
      },
      onOfflineReady() {
        // Precache complete; nothing to surface (silent by design).
      },
    });
  } catch {
    // virtual:pwa-register is unavailable (e.g. plugin disabled) — fall back
    // to a plain registration so updates still propagate.
    await navigator.serviceWorker.register("/sw.js");
  }
}
