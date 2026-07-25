// @h2fleet/db — Drizzle schema entrypoint. Re-exports every table so TS
// services can `import { telemetry, farePayments, ... } from "@h2fleet/db"`.
export * from "./schema/public";
export * from "./schema/fleet";
export * from "./schema/infra";
export * from "./schema/citizen";
export * from "./schema/commerce";
export * from "./schema/platform";
export { geometryPoint } from "./schema/columns";
