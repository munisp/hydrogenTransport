/**
 * @h2fleet/toggle-client — TypeScript SDK for the H2Fleet Feature Toggle Service.
 *
 * Contract (SPEC §3.2), identical to the Go and Python SDKs:
 *   - GET {baseUrl}/v1/toggles          -> { "toggles": { "<module>": bool, ... } }
 *   - GET {baseUrl}/v1/toggles/{module} -> { "module": "<id>", "enabled": bool, "domain": "<domain>" }
 *   - isEnabled(module) -> boolean with a 5s local cache
 *   - fail-open = false: any transport/parse/HTTP error resolves to `false` (disabled)
 */

/** The 20 module identifiers from SPEC §3.1. */
export const MODULE_IDS = [
  "telematics",
  "predictive-maintenance",
  "digital-twin",
  "fuel-monitoring",
  "route-energy-optimizer",
  "refueling-stations",
  "leak-detection",
  "dispatch-workforce",
  "compliance-reporting",
  "depot-management",
  "passenger-pwa",
  "mobile-app",
  "demand-responsive",
  "carbon-credits",
  "open-data-portal",
  "fare-payments",
  "loyalty-marketplace",
  "energy-trading",
  "gov-dashboard",
  "advertising",
] as const;

export type ModuleId = (typeof MODULE_IDS)[number];

export interface ToggleClientOptions {
  /** Local cache TTL in milliseconds. Defaults to 5000 per SPEC §3.2. */
  cacheTtlMs?: number;
  /** Per-request timeout in milliseconds. Defaults to 3000. */
  timeoutMs?: number;
  /** Optional bearer-token provider (e.g. Keycloak access token) for admin-adjacent calls. */
  getToken?: () => string | undefined;
  /** Injectable fetch implementation (tests, non-DOM runtimes). */
  fetchImpl?: typeof fetch;
  /** Clock injection for tests. */
  now?: () => number;
}

interface CacheEntry {
  enabled: boolean;
  expiresAt: number;
}

const DEFAULT_CACHE_TTL_MS = 5_000;
const DEFAULT_TIMEOUT_MS = 3_000;

export class ToggleClient {
  private readonly baseUrl: string;
  private readonly cacheTtlMs: number;
  private readonly timeoutMs: number;
  private readonly fetchImpl: typeof fetch;
  private readonly getToken?: () => string | undefined;
  private readonly now: () => number;

  private readonly cache = new Map<string, CacheEntry>();
  private readonly inFlight = new Map<string, Promise<boolean>>();
  private allCache: { toggles: Record<string, boolean>; expiresAt: number } | null = null;
  private allInFlight: Promise<Record<string, boolean>> | null = null;

  constructor(baseUrl: string, options: ToggleClientOptions = {}) {
    if (!baseUrl || typeof baseUrl !== "string") {
      throw new Error("ToggleClient: baseUrl is required");
    }
    this.baseUrl = baseUrl.replace(/\/+$/, "");
    this.cacheTtlMs = options.cacheTtlMs ?? DEFAULT_CACHE_TTL_MS;
    this.timeoutMs = options.timeoutMs ?? DEFAULT_TIMEOUT_MS;
    this.fetchImpl = options.fetchImpl ?? fetch;
    this.getToken = options.getToken;
    this.now = options.now ?? (() => Date.now());
  }

  /**
   * Returns whether a module is enabled. Fails closed: on any error the result
   * is `false` (and that negative result is cached for the TTL so a flapping
   * toggle-service does not stampede).
   */
  async isEnabled(module: string): Promise<boolean> {
    const hit = this.cache.get(module);
    if (hit && hit.expiresAt > this.now()) {
      return hit.enabled;
    }
    const pending = this.inFlight.get(module);
    if (pending) {
      return pending;
    }
    const request = this.fetchOne(module).finally(() => this.inFlight.delete(module));
    this.inFlight.set(module, request);
    return request;
  }

  /**
   * Returns the full toggle map. Fails closed to `{}` (callers should treat a
   * missing module as disabled).
   */
  async getAll(): Promise<Record<string, boolean>> {
    if (this.allCache && this.allCache.expiresAt > this.now()) {
      return { ...this.allCache.toggles };
    }
    if (this.allInFlight) {
      return this.allInFlight;
    }
    this.allInFlight = this.fetchAll().finally(() => {
      this.allInFlight = null;
    });
    return this.allInFlight;
  }

  /** Drop cached entries (one module, or everything). */
  invalidate(module?: string): void {
    if (module === undefined) {
      this.cache.clear();
      this.allCache = null;
      return;
    }
    this.cache.delete(module);
    if (this.allCache) {
      delete this.allCache.toggles[module];
    }
  }

  private async fetchOne(module: string): Promise<boolean> {
    let enabled = false;
    try {
      const res = await this.request(`/v1/toggles/${encodeURIComponent(module)}`);
      if (res.ok) {
        const body: unknown = await res.json();
        enabled =
          typeof body === "object" &&
          body !== null &&
          (body as { enabled?: unknown }).enabled === true;
      }
    } catch {
      enabled = false;
    }
    this.cache.set(module, { enabled, expiresAt: this.now() + this.cacheTtlMs });
    return enabled;
  }

  private async fetchAll(): Promise<Record<string, boolean>> {
    let toggles: Record<string, boolean> = {};
    try {
      const res = await this.request("/v1/toggles");
      if (res.ok) {
        const body: unknown = await res.json();
        const raw =
          typeof body === "object" && body !== null
            ? (body as { toggles?: unknown }).toggles
            : undefined;
        if (raw && typeof raw === "object") {
          for (const [key, value] of Object.entries(raw as Record<string, unknown>)) {
            toggles[key] = value === true;
          }
        }
      }
    } catch {
      toggles = {};
    }
    this.allCache = { toggles, expiresAt: this.now() + this.cacheTtlMs };
    return { ...toggles };
  }

  private request(path: string): Promise<Response> {
    const controller = new AbortController();
    const timer = setTimeout(() => controller.abort(), this.timeoutMs);
    const headers = new Headers({ Accept: "application/json" });
    const token = this.getToken?.();
    if (token) {
      headers.set("Authorization", `Bearer ${token}`);
    }
    return this.fetchImpl(`${this.baseUrl}${path}`, {
      method: "GET",
      headers,
      signal: controller.signal,
    }).finally(() => clearTimeout(timer));
  }
}

export default ToggleClient;
