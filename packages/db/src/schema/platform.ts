// platform schema — service-owned tables (admin-api onboarding_requests per
// its EnsureSchema; audit-log per services/go/audit-log EnsureSchema).
import { bigint, index, jsonb, pgSchema, text, timestamp, uuid } from "drizzle-orm/pg-core";

export const platform = pgSchema("platform");

export const onboardingRequests = platform.table(
  "onboarding_requests",
  {
    id: uuid("id").primaryKey().defaultRandom(),
    persona: text("persona").notNull(),
    email: text("email").notNull(),
    displayName: text("display_name").notNull(),
    org: text("org").notNull().default(""),
    status: text("status").notNull().default("pending"), // pending|approved|rejected|completed
    keycloakSub: text("keycloak_sub").notNull().default(""),
    meta: jsonb("meta").notNull().default({}),
    createdAt: timestamp("created_at", { withTimezone: true }).notNull().defaultNow(),
    decidedAt: timestamp("decided_at", { withTimezone: true }),
    decidedBy: text("decided_by").notNull().default(""),
  },
  (t) => ({
    statusIdx: index("onboarding_requests_status_idx").on(t.status),
    personaIdx: index("onboarding_requests_persona_idx").on(t.persona),
  }),
);

// Append-only hash-chained audit trail (docs/INSIDER_THREAT.md). Written
// only by the audit-log service; modeled here for read-side analytics.
export const auditLog = platform.table(
  "audit_log",
  {
    id: bigint("id", { mode: "bigint" }).primaryKey().generatedAlwaysAsIdentity(),
    actorSub: text("actor_sub").notNull(),
    actorRoles: jsonb("actor_roles").notNull().default([]),
    action: text("action").notNull(),
    entity: text("entity").notNull(),
    entityId: text("entity_id").notNull().default(""),
    before: jsonb("before"),
    after: jsonb("after"),
    ip: text("ip").notNull().default(""),
    ua: text("ua").notNull().default(""),
    ts: timestamp("ts", { withTimezone: true }).notNull().defaultNow(),
    prevHash: text("prev_hash").notNull().default(""),
    hash: text("hash").notNull().unique(),
  },
  (t) => ({
    actorIdx: index("audit_log_actor_idx").on(t.actorSub, t.ts),
    entityIdx: index("audit_log_entity_idx").on(t.entity, t.entityId, t.ts),
    tsIdx: index("audit_log_ts_idx").on(t.ts),
  }),
);
