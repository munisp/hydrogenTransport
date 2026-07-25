// commerce schema (migrations 0001_core.sql + 0003_supplemental.sql +
// 0005_missing_schemas.sql + 0006_trades_idempotency.sql).
// TigerBeetle holds the authoritative double-entry ledger; these tables are
// the query/index side (account id ranges: RIDER_WALLET=1xxx,
// OPERATOR_REVENUE=2xxx, ENERGY_TRADE=3xxx, CARBON_FUND=4xxx).
import { sql } from "drizzle-orm";
import {
  bigint,
  boolean,
  check,
  index,
  integer,
  numeric,
  pgSchema,
  text,
  timestamp,
  uniqueIndex,
  uuid,
} from "drizzle-orm/pg-core";

import { vehicles } from "./fleet";

export const commerce = pgSchema("commerce");

export const farePayments = commerce.table(
  "fare_payments",
  {
    id: uuid("id").primaryKey().defaultRandom(),
    riderSub: text("rider_sub").notNull(), // Keycloak subject
    amountMinor: bigint("amount_minor", { mode: "bigint" }).notNull(),
    currency: text("currency").notNull().default("EUR"),
    mojaloopTransferId: text("mojaloop_transfer_id"),
    status: text("status").notNull().default("initiated"), // initiated|settled|failed|refunded
    createdAt: timestamp("created_at", { withTimezone: true }).notNull().defaultNow(),
    idempotencyKey: text("idempotency_key"),
    tbTransferId: text("tb_transfer_id"),
    // 0005: refund linkage (refund_of points at the original payment)
    refundOf: uuid("refund_of"),
    refundedAt: timestamp("refunded_at", { withTimezone: true }),
  },
  (t) => ({
    riderIdx: index("fare_payments_rider_idx").on(t.riderSub, t.createdAt),
    idempotencyKeyUq: uniqueIndex("fare_payments_idempotency_key_uq").on(t.idempotencyKey),
  }),
);

export const trades = commerce.table(
  "trades",
  {
    id: uuid("id").primaryKey().defaultRandom(),
    kind: text("kind").notNull(), // h2-sale|h2-purchase|energy-export
    quantityKg: numeric("quantity_kg").notNull(),
    priceMinor: bigint("price_minor", { mode: "bigint" }).notNull(),
    status: text("status").notNull().default("executed"), // proposed|executed|failed (handler always sets explicitly)
    createdAt: timestamp("created_at", { withTimezone: true }).notNull().defaultNow(),
    tbTransferId: text("tb_transfer_id"), // 0005: TigerBeetle transfer reconciliation
    // 0006: Idempotency-Key header value; the DB unique index is partial
    // (WHERE idempotency_key IS NOT NULL) — Drizzle models it unqualified,
    // same as fare_payments_idempotency_key_uq above.
    idempotencyKey: text("idempotency_key"),
  },
  (t) => ({
    idempotencyKeyUq: uniqueIndex("trades_idempotency_key_uq").on(t.idempotencyKey),
  }),
);

// Loyalty (0005): account balances keyed by rider_sub plus the accrual
// ledger and redemption records that make accrual/audit possible.
export const loyaltyAccounts = commerce.table(
  "loyalty_accounts",
  {
    riderSub: text("rider_sub").primaryKey(),
    points: integer("points").notNull().default(0),
    updatedAt: timestamp("updated_at", { withTimezone: true }).notNull().defaultNow(),
  },
  (t) => ({
    pointsNonNegative: check("loyalty_accounts_points_check", sql`${t.points} >= 0`),
  }),
);

export const loyaltyLedger = commerce.table(
  "loyalty_ledger",
  {
    id: uuid("id").primaryKey(),
    riderSub: text("rider_sub").notNull(),
    delta: integer("delta").notNull(),
    reason: text("reason").notNull(),
    refId: text("ref_id").notNull().unique(),
    createdAt: timestamp("created_at", { withTimezone: true }).notNull().defaultNow(),
  },
  (t) => ({
    riderIdx: index("loyalty_ledger_rider_idx").on(t.riderSub, t.createdAt),
  }),
);

export const loyaltyRedemptions = commerce.table(
  "loyalty_redemptions",
  {
    id: uuid("id").primaryKey(),
    riderSub: text("rider_sub").notNull(),
    offerId: uuid("offer_id").notNull(),
    pointsSpent: integer("points_spent").notNull(),
    idempotencyKey: text("idempotency_key").notNull().unique(),
    status: text("status").notNull().default("completed"),
    createdAt: timestamp("created_at", { withTimezone: true }).notNull().defaultNow(),
  },
  (t) => ({
    riderIdx: index("loyalty_redemptions_rider_idx").on(t.riderSub, t.createdAt),
  }),
);

export const marketplaceOffers = commerce.table("marketplace_offers", {
  id: uuid("id").primaryKey().defaultRandom(),
  title: text("title").notNull(),
  description: text("description").notNull().default(""),
  partner: text("partner").notNull().default(""),
  costPoints: integer("cost_points").notNull(),
  active: boolean("active").notNull().default(true),
  createdAt: timestamp("created_at", { withTimezone: true }).notNull().defaultNow(),
});

export const adCampaigns = commerce.table("ad_campaigns", {
  id: uuid("id").primaryKey().defaultRandom(),
  name: text("name").notNull(),
  advertiser: text("advertiser").notNull().default(""),
  budgetMinor: bigint("budget_minor", { mode: "bigint" }).notNull().default(0n),
  status: text("status").notNull().default("draft"),
  startsAt: timestamp("starts_at", { withTimezone: true }),
  endsAt: timestamp("ends_at", { withTimezone: true }),
  createdAt: timestamp("created_at", { withTimezone: true }).notNull().defaultNow(),
});

// Ad inventory + placements (0005). Overlap double-booking is prevented in
// the database by the `ad_placements_no_overlap` exclusion constraint
// (btree_gist) — modeled here as a comment; exclusion constraints have no
// Drizzle table DSL.
export const adInventory = commerce.table("ad_inventory", {
  id: uuid("id").primaryKey().defaultRandom(),
  kind: text("kind").notNull(), // bus-side|bus-interior|shelter|digital-screen
  busId: uuid("bus_id").references(() => vehicles.id), // NULL = off-bus inventory
  label: text("label").notNull().default(""),
  active: boolean("active").notNull().default(true),
  createdAt: timestamp("created_at", { withTimezone: true }).notNull().defaultNow(),
});

export const adPlacements = commerce.table("ad_placements", {
  id: uuid("id").primaryKey().defaultRandom(),
  campaignId: uuid("campaign_id").notNull().references(() => adCampaigns.id),
  inventoryId: uuid("inventory_id").notNull().references(() => adInventory.id),
  startsAt: timestamp("starts_at", { withTimezone: true }).notNull(),
  endsAt: timestamp("ends_at", { withTimezone: true }).notNull(),
  createdAt: timestamp("created_at", { withTimezone: true }).notNull().defaultNow(),
});

export const riderAccounts = commerce.table("rider_accounts", {
  riderSub: text("rider_sub").primaryKey(),
  accountId: bigint("account_id", { mode: "bigint" }).unique(),
  createdAt: timestamp("created_at", { withTimezone: true }).notNull().defaultNow(),
});
