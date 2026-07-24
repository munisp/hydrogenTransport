import { describe, expect, it, vi } from "vitest";
import { MODULE_IDS, ToggleClient } from "./index";

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

describe("ToggleClient", () => {
  it("exposes the 20 module ids from SPEC §3.1", () => {
    expect(MODULE_IDS).toHaveLength(20);
    expect(MODULE_IDS).toContain("telematics");
    expect(MODULE_IDS).toContain("advertising");
  });

  it("requires a baseUrl", () => {
    expect(() => new ToggleClient("")).toThrow();
  });

  it("resolves enabled=true from the service", async () => {
    const fetchImpl = vi
      .fn()
      .mockResolvedValue(jsonResponse({ module: "telematics", enabled: true, domain: "fleet" }));
    const client = new ToggleClient("http://toggle:8080/", { fetchImpl });
    await expect(client.isEnabled("telematics")).resolves.toBe(true);
    expect(fetchImpl).toHaveBeenCalledWith(
      "http://toggle:8080/v1/toggles/telematics",
      expect.objectContaining({ method: "GET" }),
    );
  });

  it("caches results for the TTL and dedupes concurrent calls", async () => {
    let now = 1_000;
    const fetchImpl = vi
      .fn()
      .mockResolvedValue(jsonResponse({ module: "carbon-credits", enabled: true, domain: "citizen" }));
    const client = new ToggleClient("http://toggle:8080", { fetchImpl, now: () => now });

    const [a, b] = await Promise.all([client.isEnabled("carbon-credits"), client.isEnabled("carbon-credits")]);
    expect(a).toBe(true);
    expect(b).toBe(true);
    expect(fetchImpl).toHaveBeenCalledTimes(1);

    await client.isEnabled("carbon-credits");
    expect(fetchImpl).toHaveBeenCalledTimes(1);

    now += 5_001; // past the 5s TTL
    await client.isEnabled("carbon-credits");
    expect(fetchImpl).toHaveBeenCalledTimes(2);
  });

  it("fails closed on HTTP errors", async () => {
    const fetchImpl = vi.fn().mockResolvedValue(jsonResponse({ error: "boom" }, 500));
    const client = new ToggleClient("http://toggle:8080", { fetchImpl });
    await expect(client.isEnabled("gov-dashboard")).resolves.toBe(false);
  });

  it("fails closed on network errors", async () => {
    const fetchImpl = vi.fn().mockRejectedValue(new TypeError("fetch failed"));
    const client = new ToggleClient("http://toggle:8080", { fetchImpl });
    await expect(client.isEnabled("gov-dashboard")).resolves.toBe(false);
  });

  it("getAll returns the toggle map and fails closed to {}", async () => {
    const okFetch = vi.fn().mockResolvedValue(
      jsonResponse({ toggles: { telematics: true, "leak-detection": false } }),
    );
    const client = new ToggleClient("http://toggle:8080", { fetchImpl: okFetch });
    await expect(client.getAll()).resolves.toEqual({ telematics: true, "leak-detection": false });

    const badFetch = vi.fn().mockRejectedValue(new Error("down"));
    const failing = new ToggleClient("http://toggle:8080", { fetchImpl: badFetch });
    await expect(failing.getAll()).resolves.toEqual({});
  });

  it("sends a bearer token when a provider is configured", async () => {
    const fetchImpl = vi
      .fn()
      .mockResolvedValue(jsonResponse({ module: "advertising", enabled: false, domain: "commerce" }));
    const client = new ToggleClient("http://toggle:8080", {
      fetchImpl,
      getToken: () => "test-token",
    });
    await client.isEnabled("advertising");
    const headers = fetchImpl.mock.calls[0]?.[1]?.headers as Headers;
    expect(headers.get("Authorization")).toBe("Bearer test-token");
  });

  it("invalidate drops cached state", async () => {
    const fetchImpl = vi
      .fn()
      .mockResolvedValue(jsonResponse({ module: "telematics", enabled: true, domain: "fleet" }));
    const client = new ToggleClient("http://toggle:8080", { fetchImpl });
    await client.isEnabled("telematics");
    client.invalidate("telematics");
    await client.isEnabled("telematics");
    expect(fetchImpl).toHaveBeenCalledTimes(2);
  });
});
