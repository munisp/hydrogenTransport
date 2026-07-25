// Vitest: analytics endpoints with a mocked pg client (no live database) and
// real JWT verification through the jose middleware (local JWKS, RS256).
import { beforeAll, describe, expect, it, vi } from "vitest";
import { drizzle } from "drizzle-orm/node-postgres";
import { exportJWK, generateKeyPair, SignJWT } from "jose";
import { createLocalJWKSet } from "jose";
import type { JWTPayload, KeyLike } from "jose";

import { buildApp } from "../src/app";

// ------------------------------------------------------------ mocked pg ----
// Queue of result sets; each db.execute() shifts one. Assert order matches
// the handler's query sequence.
function mockDb(resultSets: Record<string, unknown>[][]) {
  const query = vi.fn();
  for (const rows of resultSets) {
    query.mockResolvedValueOnce({ rows });
  }
  query.mockResolvedValue({ rows: [] });
  return { db: drizzle({ query } as never), query };
}

// ------------------------------------------------------------- JWT stub ----
// Issue real RS256 tokens and serve them through a local JWKS — exercises
// jwtAuth's issuer/audience/role semantics end-to-end.
let privateKey: KeyLike;
const ISSUER = "https://kc.test/realms/h2fleet";

vi.mock("jose", async (importOriginal) => {
  const mod = await importOriginal<typeof import("jose")>();
  return {
    ...mod,
    createRemoteJWKSet: () => localJwks,
  };
});

let localJwks: ReturnType<typeof createLocalJWKSet>;

async function token(claims: JWTPayload): Promise<string> {
  return await new SignJWT(claims)
    .setProtectedHeader({ alg: "RS256", kid: "test" })
    .setIssuer(ISSUER)
    .setAudience("services")
    .setIssuedAt()
    .setExpirationTime("5m")
    .sign(privateKey);
}

beforeAll(async () => {
  const pair = await generateKeyPair("RS256");
  privateKey = pair.privateKey as KeyLike;
  const jwk = await exportJWK(pair.publicKey);
  localJwks = createLocalJWKSet({ keys: [{ ...jwk, kid: "test", alg: "RS256" }] });
});

function app(db: ReturnType<typeof mockDb>["db"]) {
  return buildApp({
    db,
    authCfg: { issuer: ISSUER, issuerAlt: "", audience: "services" },
    ping: async () => {},
  });
}

const operatorToken = () =>
  token({ sub: "op-1", realm_access: { roles: ["operator"] } });

// ---------------------------------------------------------------- tests ----
describe("analytics-bff", () => {
  it("healthz is public", async () => {
    const res = await app(mockDb([]).db).request("/healthz");
    expect(res.status).toBe(200);
    expect(await res.json()).toEqual({ status: "ok" });
  });

  it("metrics exposes the prom-client registry", async () => {
    const res = await app(mockDb([]).db).request("/metrics");
    expect(res.status).toBe(200);
    expect(await res.text()).toContain("process_");
  });

  it("analytics routes reject anonymous requests", async () => {
    const res = await app(mockDb([]).db).request("/v1/analytics/fleet-summary");
    expect(res.status).toBe(401);
  });

  it("analytics routes reject tokens without an allowed role", async () => {
    const bad = await token({ sub: "cit-1", realm_access: { roles: ["citizen"] } });
    const res = await app(mockDb([]).db).request("/v1/analytics/fleet-summary", {
      headers: { authorization: `Bearer ${bad}` },
    });
    expect(res.status).toBe(403);
  });

  it("fleet-summary aggregates vehicles, latest H2 and stations", async () => {
    const { db, query } = mockDb([
      [
        { status: "active", count: 47 },
        { status: "maintenance", count: 3 },
      ],
      [{ avg_h2: 61.5 }],
      [{ total: 3, available_kg: 812.4 }],
    ]);
    const res = await app(db).request("/v1/analytics/fleet-summary", {
      headers: { authorization: `Bearer ${await operatorToken()}` },
    });
    expect(res.status).toBe(200);
    const body = (await res.json()) as any;
    expect(body.buses).toEqual({ total: 50, by_status: { active: 47, maintenance: 3 } });
    expect(body.avg_h2_level_pct).toBeCloseTo(61.5);
    expect(body.stations).toEqual({ total: 3, available_kg: 812.4 });
    // queries hit the schema-qualified tables from @h2fleet/db
    const sqls = query.mock.calls.map((c) => (c[0] as { text: string }).text).join("\n");
    expect(sqls).toContain('"fleet"."vehicles"');
    expect(sqls).toContain('"fleet"."telemetry"');
    expect(sqls).toContain('"infra"."stations"');
    expect(sqls).toContain("distinct on");
  });

  it("revenue-daily merges fares and trades per day", async () => {
    const { db } = mockDb([
      [
        { day: "2025-06-01", payments: 120, settled_minor: 543210.0 },
        { day: "2025-06-02", payments: 98, settled_minor: 401000.0 },
      ],
      [{ day: "2025-06-01", trades: 4, trades_minor: 99000.0 }],
    ]);
    const res = await app(db).request("/v1/analytics/revenue-daily?days=7", {
      headers: { authorization: `Bearer ${await operatorToken()}` },
    });
    expect(res.status).toBe(200);
    const body = (await res.json()) as any;
    expect(body.days).toBe(7);
    expect(body.rows).toHaveLength(2);
    expect(body.rows[0]).toEqual({
      day: "2025-06-01",
      payments: 120,
      settled_minor: 543210,
      trades: 4,
      trades_minor: 99000,
    });
    expect(body.rows[1].trades).toBe(0);
  });

  it("carbon-daily returns zod-validated rows", async () => {
    const { db } = mockDb([
      [{ day: "2025-06-01", kg: 1250.75, credits: 125.07 }],
    ]);
    const res = await app(db).request("/v1/analytics/carbon-daily", {
      headers: { authorization: `Bearer ${await operatorToken()}` },
    });
    expect(res.status).toBe(200);
    const body = (await res.json()) as any;
    expect(body.days).toBe(30); // default window
    expect(body.rows[0]).toEqual({ day: "2025-06-01", kg_co2_avoided: 1250.75, credits: 125.07 });
  });

  it("days parameter is clamped to 1..365 with 30 default on garbage", async () => {
    const { db } = mockDb([[]]);
    const res = await app(db).request("/v1/analytics/carbon-daily?days=notanum", {
      headers: { authorization: `Bearer ${await operatorToken()}` },
    });
    expect(res.status).toBe(200);
    expect(((await res.json()) as any).days).toBe(30);
  });
});
