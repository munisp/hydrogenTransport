/**
 * API shapes mirrored from SPEC §3.4 (Postgres schemas) and the domain service
 * contracts. Field names follow the snake_case JSON emitted by the Go services.
 */

export interface GeoPoint {
  lat: number;
  lon: number;
}

// ---- Fleet domain (fleet-api, /api/fleet) -----------------------------------

/** Wave-5 multi-energy discriminator (migration 0008). Absent ⇒ legacy "h2". */
export type EnergyType = "h2" | "battery" | "diesel" | "cng";

/** fleet.vehicles */
export interface Vehicle {
  id: string;
  fleet_no: string;
  vin: string;
  model: string;
  h2_capacity_kg: number;
  energy_type?: EnergyType;
  status: "in_service" | "refueling" | "maintenance" | "depot" | "offline" | string;
  lat?: number | null;
  lon?: number | null;
}

/** fleet-api GET /v1/telemetry/latest — latest sample per bus. */
export interface TelemetrySample {
  bus_id: string;
  ts: string;
  speed_kph: number;
  h2_level_pct: number;
  fuel_cell_kw: number;
  battery_soc_pct: number;
  odometer_km: number;
  /** Wave-5 generic energy fields (h2 rows backfill these from the h2 columns). */
  energy_level_pct?: number;
  powertrain_kw?: number;
  energy_type?: EnergyType;
  lat?: number | null;
  lon?: number | null;
}

/** fleet.maintenance_predictions */
export interface MaintenancePrediction {
  id: string;
  bus_id: string;
  component: string;
  risk_score: number; // 0..1
  predicted_failure_at: string;
  model_version: string;
  created_at: string;
}

/** fleet-api GET /v1/fuel/levels — latest H2 level per vehicle. */
export interface FuelReading {
  bus_id: string;
  fleet_no: string;
  h2_level_pct: number;
  h2_remaining_kg: number;
  energy_type?: EnergyType;
  energy_level_pct?: number;
  /** Generic remaining energy (kg | kWh | liters depending on energy_type). */
  energy_remaining?: number;
  estimated_range_km: number;
  measured_at: string;
}

// ---- Digital twin (/api/twin, Rust hot path) --------------------------------

/** digital-twin (Rust) TwinState — GET /v1/twin/{bus_id}. */
export interface TwinState {
  bus_id: string;
  ts: string;
  speed_kph: number;
  h2_level_pct: number;
  fuel_cell_kw: number;
  battery_soc_pct: number;
  odometer_km: number;
  /** Wave-5 generic energy fields. */
  energy_level_pct?: number;
  powertrain_kw?: number;
  energy_type?: EnergyType;
  lat: number;
  lon: number;
  route_id?: string | null;
  depot_id?: string | null;
  heading_deg?: number | null;
  /** Derived: moving | idle | refueling */
  status: string;
  updated_at: string;
}

/** predictive-maintenance POST /v1/predict response. */
export interface ComponentPrediction {
  component: string;
  risk_score: number; // 0..1
  predicted_failure_at: string;
}

export interface PredictResponse {
  bus_id: string;
  model_version: string;
  feature_window_hours: number;
  predictions: ComponentPrediction[];
}

// ---- Route optimizer (/api/optimize) ----------------------------------------

/** route-optimizer POST /v1/optimize/route request. */
export interface OptimizationRequest {
  bus_ids?: string[] | null; // null/omitted = all buses with telemetry
  date: string; // YYYY-MM-DD
}

export interface RefuelEvent {
  station_id: string;
  station_name: string;
  kg_taken: number;
  at_stop_sequence: number;
  remaining_range_km_before: number;
}

export interface PlanLeg {
  sequence: number;
  stop_id: string;
  stop_name: string;
  cumulative_km: number;
}

export interface BusPlan {
  bus_id: string;
  fleet_no: string;
  feasible: boolean;
  notes: string[];
  total_route_km: number;
  h2_start_kg: number;
  h2_end_kg: number;
  range_start_km: number;
  legs: PlanLeg[];
  refuels: RefuelEvent[];
}

/** route-optimizer POST /v1/optimize/route response. */
export interface OptimizationPlan {
  date: string;
  data_source: string; // "database" | "seed"
  solver_status: string;
  unassigned_stops: string[];
  plans: BusPlan[];
}

// ---- Infra domain (infra-api, /api/infra) -----------------------------------

/** infra.stations */
export interface Station {
  id: string;
  name: string;
  capacity_kg: number;
  available_kg: number;
  status: "online" | "degraded" | "offline" | "maintenance" | string;
  geom?: GeoPoint | null;
  queue_length?: number;
}

/** infra.incidents */
export interface Incident {
  id: string;
  type: "leak" | "collision" | "breakdown" | "station_fault" | "security" | string;
  severity: "low" | "medium" | "high" | "critical" | string;
  bus_id: string | null;
  station_id: string | null;
  status: "open" | "acknowledged" | "resolved" | string;
  opened_at: string;
  meta?: Record<string, unknown>;
}

/** infra-api DispatchJob — mirrors infra.dispatch_jobs. */
export interface DispatchJob {
  id: string;
  driver_sub: string;
  vehicle_id?: string | null; // omitempty
  route: string;
  starts_at?: string | null; // omitempty
  status: "assigned" | "accepted" | "in_progress" | "completed" | "cancelled" | string;
  created_at: string;
  accepted_at?: string | null; // omitempty
}

/** infra-api ComplianceReport — {id, generated_at, report(jsonb)}. */
export interface ComplianceReport {
  id: string;
  generated_at: string;
  report: Record<string, unknown>;
}

export interface DepotBay {
  id: string;
  depot: string;
  label: string;
  kind: "fueling" | "charging" | "parking" | "workshop" | string;
  occupied_by: string | null;
  status: "free" | "occupied" | "out_of_service" | string;
}

export interface WorkOrder {
  id: string;
  bus_id: string | null;
  asset: string;
  description: string;
  priority: "low" | "medium" | "high" | string;
  status: "open" | "in_progress" | "done" | string;
  opened_at: string;
}

// ---- Citizen domain (citizen-api, /api/citizen) -----------------------------

/** citizen-api GET /v1/passenger/stops — GTFS stops.txt style. */
export interface Stop {
  stop_id: string;
  stop_name: string;
  stop_lat: number;
  stop_lon: number;
}

/** citizen-api GET /v1/passenger/arrivals — one upcoming departure at a stop. */
export interface Arrival {
  route_id: string;
  route_short_name: string;
  headsign: string;
  stop_id: string;
  scheduled_at: string;
  in_minutes: number;
}

/** citizen-api GET /v1/passenger/alerts — GTFS-RT style service alert. */
export interface ServiceAlert {
  id: string;
  header: string;
  description: string;
  severity: "info" | "warning" | "severe" | string;
  route_ids?: string[]; // omitempty
  active_from: string;
  active_until: string;
}

/** citizen-api GET /v1/passenger/journey — one direct-ride option. */
export interface JourneyOption {
  route_id: string;
  route_short_name: string;
  from_stop_id: string;
  to_stop_id: string;
  depart_at: string;
  arrive_at: string;
  duration_min: number;
}

/** citizen-api DRTRequest — mirrors citizen.drt_requests; pickup/dropoff are
 * emitted as scalar lat/lon fields (omitempty, PostGIS-derived). */
export interface DrtRequest {
  id: string;
  user_sub: string;
  pickup_lat?: number | null;
  pickup_lon?: number | null;
  dropoff_lat?: number | null;
  dropoff_lon?: number | null;
  status: "requested" | "matched" | "en_route" | "completed" | "cancelled" | string;
  requested_at: string;
}

/** citizen.carbon_credits */
export interface CarbonCredit {
  id: string;
  period: string;
  kg_co2_avoided: number;
  credits: number;
  issued_at: string;
}

export interface OpenDataset {
  id: string;
  name: string;
  kind: "gtfs" | "gtfs-rt" | "telemetry" | "carbon" | string;
  description: string;
  url: string;
  updated_at: string;
}

// ---- Commerce domain (commerce-api, /api/commerce) --------------------------

/** commerce-api GET /v1/gov/kpis — aggregated KPIs for the gov-dashboard module. */
/**
 * GovKPIs — every rollup is independently nullable: a failed source leaves its
 * fields null and is named in `degraded` (never a fabricated value).
 * `fleet_uptime_pct` is null until a time-based availability source exists.
 */
export interface GovKpis {
  revenue_30d_minor: number | null;
  settled_payments_30d: number | null;
  ridership_estimate_30d: number | null;
  kg_co2_avoided_total: number | null;
  carbon_credits_total: number | null;
  vehicles_total: number | null;
  vehicles_active: number | null;
  /** Static status mix (active/total), NOT time-based uptime. */
  fleet_active_ratio_pct: number | null;
  /** Null until a time-based availability source exists. */
  fleet_uptime_pct: number | null;
  fleet_uptime_note?: string;
  stations_available_kg: number | null;
  /** open + acknowledged + in_progress */
  open_incidents: number | null;
  partial?: boolean;
  degraded?: string[];
}

/** commerce.fare_payments */
export interface FarePayment {
  id: string;
  rider_sub: string;
  amount_minor: number;
  currency: string;
  mojaloop_transfer_id: string | null;
  status: "initiated" | "settled" | "failed" | "refunded" | string;
  created_at: string;
}

export interface MarketplaceOffer {
  id: string;
  business: string;
  title: string;
  description: string;
  cost_points: number;
  active: boolean;
}

/** commerce.trades */
export interface EnergyTrade {
  id: string;
  kind: "h2-sale" | "h2-purchase" | "energy-export" | string;
  quantity_kg: number;
  price_minor: number;
  status: "proposed" | "executed" | "failed" | string;
  /** TigerBeetle transfer id, set once executed. */
  tb_transfer_id?: string | null;
  idempotency_key?: string;
  created_at: string;
}

export interface AdCampaign {
  id: string;
  name: string;
  advertiser: string;
  placement: "bus_exterior" | "bus_interior" | "pwa" | "station_screen" | string;
  status: "draft" | "active" | "paused" | "ended" | string;
  impressions: number;
  budget_minor: number;
  currency: string;
  starts_at: string;
  ends_at: string;
}
