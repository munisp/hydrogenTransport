import { lazy, type LazyExoticComponent, type ComponentType } from "react";
import type { LucideIcon } from "lucide-react";
import {
  BarChart3,
  Bus,
  CalendarClock,
  Cpu,
  CreditCard,
  Database,
  FileCheck,
  Fuel,
  Gauge,
  Leaf,
  Map as MapIcon,
  Megaphone,
  Navigation,
  Route as RouteIcon,
  ShieldAlert,
  Smartphone,
  Store,
  Warehouse,
  Wrench,
  Zap,
} from "lucide-react";
import type { MODULE_IDS } from "@h2fleet/toggle-client";

export type ModuleId = (typeof MODULE_IDS)[number];
export type DomainId = "fleet" | "infra" | "citizen" | "commerce";

export interface DomainMeta {
  id: DomainId;
  label: string;
  description: string;
}

export const DOMAINS: DomainMeta[] = [
  { id: "fleet", label: "Fleet Operations", description: "Telematics, twins, maintenance and energy for the 50-bus H2 fleet." },
  { id: "infra", label: "Infrastructure & Safety", description: "Refueling stations, leak detection, dispatch, compliance and depot." },
  { id: "citizen", label: "Citizen & Engagement", description: "Passenger information, DRT, carbon credits and open data." },
  { id: "commerce", label: "Commerce & Finance", description: "Fares, marketplace, energy trading, KPIs and advertising." },
];

export interface ModuleDef {
  /** Exact identifier from SPEC §3.1. */
  id: ModuleId;
  domain: DomainId;
  label: string;
  description: string;
  path: string;
  icon: LucideIcon;
  /** Lazy page bundle — each module code-splits independently (SPEC §3.7). */
  component: LazyExoticComponent<ComponentType>;
}

/**
 * The module registry: all 20 module ids → lazy React pages, grouped by domain.
 * Nav entries and routes are filtered by the feature-toggle state at runtime.
 */
export const MODULES: ModuleDef[] = [
  // ---- Domain 1: Fleet Operations & Telematics ----
  {
    id: "telematics",
    domain: "fleet",
    label: "Live Fleet Map",
    description: "Real-time vehicle telemetry ingestion & live map of all 50 buses.",
    path: "/fleet/map",
    icon: MapIcon,
    component: lazy(() => import("../pages/fleet/LiveFleetMapPage")),
  },
  {
    id: "predictive-maintenance",
    domain: "fleet",
    label: "Maintenance",
    description: "ML failure-risk prediction on fuel-cell, battery and H2 systems.",
    path: "/fleet/maintenance",
    icon: Wrench,
    component: lazy(() => import("../pages/fleet/MaintenancePage")),
  },
  {
    id: "digital-twin",
    domain: "fleet",
    label: "Digital Twin",
    description: "Per-bus digital twin state from the Rust hot-path engine.",
    path: "/fleet/twin",
    icon: Cpu,
    component: lazy(() => import("../pages/fleet/DigitalTwinPage")),
  },
  {
    id: "fuel-monitoring",
    domain: "fleet",
    label: "Fuel Monitoring",
    description: "H2 tank levels, consumption and range prediction per bus.",
    path: "/fleet/fuel",
    icon: Fuel,
    component: lazy(() => import("../pages/fleet/FuelMonitoringPage")),
  },
  {
    id: "route-energy-optimizer",
    domain: "fleet",
    label: "Route Optimizer",
    description: "Route + refueling schedule optimization (OR-Tools).",
    path: "/fleet/optimizer",
    icon: RouteIcon,
    component: lazy(() => import("../pages/fleet/RouteOptimizerPage")),
  },

  // ---- Domain 2: Infrastructure & Safety ----
  {
    id: "refueling-stations",
    domain: "infra",
    label: "Stations",
    description: "Station status, queue management and H2 inventory.",
    path: "/infra/stations",
    icon: Gauge,
    component: lazy(() => import("../pages/infra/StationsPage")),
  },
  {
    id: "leak-detection",
    domain: "infra",
    label: "Safety & Leaks",
    description: "H2 leak sensor ingestion, alarms and incident workflow.",
    path: "/infra/safety",
    icon: ShieldAlert,
    component: lazy(() => import("../pages/infra/SafetyPage")),
  },
  {
    id: "dispatch-workforce",
    domain: "infra",
    label: "Dispatch",
    description: "Driver scheduling & dispatch backed by Temporal workflows.",
    path: "/infra/dispatch",
    icon: CalendarClock,
    component: lazy(() => import("../pages/infra/DispatchPage")),
  },
  {
    id: "compliance-reporting",
    domain: "infra",
    label: "Compliance",
    description: "Regulatory & safety compliance reports.",
    path: "/infra/compliance",
    icon: FileCheck,
    component: lazy(() => import("../pages/infra/CompliancePage")),
  },
  {
    id: "depot-management",
    domain: "infra",
    label: "Depot",
    description: "Depot bays, charging/fueling assets and work orders.",
    path: "/infra/depot",
    icon: Warehouse,
    component: lazy(() => import("../pages/infra/DepotPage")),
  },

  // ---- Domain 3: Citizen & Engagement ----
  {
    id: "passenger-pwa",
    domain: "citizen",
    label: "Passenger",
    description: "Arrivals board, journey planner and service alerts.",
    path: "/citizen/passenger",
    icon: Bus,
    component: lazy(() => import("../pages/citizen/PassengerPage")),
  },
  {
    id: "mobile-app",
    domain: "citizen",
    label: "Driver Portal",
    description: "Driver-facing surface of the Expo mobile app: assigned jobs and incident reporting.",
    path: "/citizen/driver",
    icon: Smartphone,
    component: lazy(() => import("../pages/citizen/DriverPortalPage")),
  },
  {
    id: "demand-responsive",
    domain: "citizen",
    label: "On-Demand (DRT)",
    description: "Demand-responsive shuttle requests.",
    path: "/citizen/drt",
    icon: Navigation,
    component: lazy(() => import("../pages/citizen/DrtPage")),
  },
  {
    id: "carbon-credits",
    domain: "citizen",
    label: "Carbon Credits",
    description: "CO2 avoided accounting and credit issuance history.",
    path: "/citizen/carbon",
    icon: Leaf,
    component: lazy(() => import("../pages/citizen/CarbonPage")),
  },
  {
    id: "open-data-portal",
    domain: "citizen",
    label: "Open Data",
    description: "GTFS/GTFS-RT feeds and open datasets.",
    path: "/citizen/open-data",
    icon: Database,
    component: lazy(() => import("../pages/citizen/OpenDataPage")),
  },

  // ---- Domain 4: Commerce & Finance ----
  {
    id: "fare-payments",
    domain: "commerce",
    label: "Payments",
    description: "Fare collection over Mojaloop rails with the TigerBeetle ledger.",
    path: "/commerce/payments",
    icon: CreditCard,
    component: lazy(() => import("../pages/commerce/PaymentsPage")),
  },
  {
    id: "loyalty-marketplace",
    domain: "commerce",
    label: "Marketplace",
    description: "Citizen rewards and the local business marketplace.",
    path: "/commerce/marketplace",
    icon: Store,
    component: lazy(() => import("../pages/commerce/MarketplacePage")),
  },
  {
    id: "energy-trading",
    domain: "commerce",
    label: "Energy Trading",
    description: "Surplus H2 / energy trading ledger.",
    path: "/commerce/energy",
    icon: Zap,
    component: lazy(() => import("../pages/commerce/EnergyTradingPage")),
  },
  {
    id: "gov-dashboard",
    domain: "commerce",
    label: "Gov Dashboard",
    description: "City KPI dashboard: cost, emissions, ridership, uptime.",
    path: "/commerce/dashboard",
    icon: BarChart3,
    component: lazy(() => import("../pages/commerce/GovDashboardPage")),
  },
  {
    id: "advertising",
    domain: "commerce",
    label: "Advertising",
    description: "On-bus and digital ad inventory & campaigns.",
    path: "/commerce/ads",
    icon: Megaphone,
    component: lazy(() => import("../pages/commerce/AdvertisingPage")),
  },
];

export function modulesByDomain(domain: DomainId): ModuleDef[] {
  return MODULES.filter((m) => m.domain === domain);
}

export function moduleById(id: string): ModuleDef | undefined {
  return MODULES.find((m) => m.id === id);
}
