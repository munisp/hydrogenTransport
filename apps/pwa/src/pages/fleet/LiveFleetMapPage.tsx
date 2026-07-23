import { useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import maplibregl, { Map as MapLibreMap, Marker } from "maplibre-gl";
import "maplibre-gl/dist/maplibre-gl.css";
import { useQuery } from "@tanstack/react-query";
import { X } from "lucide-react";
import { getTwin, latestTelemetry, listVehicles } from "../../api/fleet";
import type { TelemetrySample, Vehicle } from "../../api/types";
import { config } from "../../config";
import {
  Badge,
  Card,
  CardContent,
  CardHeader,
  CardTitle,
  ErrorState,
  PageHeader,
  ProgressBar,
  Spinner,
  StatusBadge,
} from "../../components/ui";
import { formatDateTime, formatNumber } from "../../lib/format";
import { cn } from "../../lib/utils";

const STATUS_COLORS: Record<string, string> = {
  in_service: "#0f766e",
  refueling: "#b45309",
  maintenance: "#dc2626",
  depot: "#78716c",
  offline: "#a8a29e",
};

interface BusOnMap {
  vehicle: Vehicle;
  telemetry?: TelemetrySample;
  lat: number;
  lon: number;
}

/** telematics — live map of the 50-bus fleet; clicking a bus opens its twin panel. */
export default function LiveFleetMapPage() {
  const mapContainer = useRef<HTMLDivElement | null>(null);
  const mapRef = useRef<MapLibreMap | null>(null);
  const markersRef = useRef<Marker[]>([]);
  const [selected, setSelected] = useState<BusOnMap | null>(null);

  const vehiclesQuery = useQuery({ queryKey: ["fleet", "vehicles"], queryFn: listVehicles });
  const telemetryQuery = useQuery({
    queryKey: ["fleet", "telemetry", "latest"],
    queryFn: latestTelemetry,
    refetchInterval: 10_000,
  });

  const buses = useMemo<BusOnMap[]>(() => {
    const vehicles = vehiclesQuery.data ?? [];
    const telemetry = new Map((telemetryQuery.data ?? []).map((t) => [t.bus_id, t]));
    return vehicles.flatMap((vehicle) => {
      const t = telemetry.get(vehicle.id);
      const lat = t?.lat ?? vehicle.lat;
      const lon = t?.lon ?? vehicle.lon;
      if (lat === undefined || lon === undefined || lat === null || lon === null) return [];
      return [{ vehicle, telemetry: t, lat, lon }];
    });
  }, [vehiclesQuery.data, telemetryQuery.data]);

  // Initialise the map once.
  useEffect(() => {
    if (!mapContainer.current || mapRef.current) return;
    const map = new maplibregl.Map({
      container: mapContainer.current,
      style: config.mapStyleUrl,
      center: [14.42, 50.08], // depot centroid; refined by fitBounds below
      zoom: 11,
      attributionControl: { compact: true },
    });
    map.addControl(new maplibregl.NavigationControl({ visualizePitch: true }), "top-right");
    mapRef.current = map;
    return () => {
      map.remove();
      mapRef.current = null;
    };
  }, []);

  // Sync markers with the latest vehicle/telemetry merge.
  useEffect(() => {
    const map = mapRef.current;
    if (!map) return;
    markersRef.current.forEach((m) => m.remove());
    markersRef.current = buses.map((bus) => {
      const el = document.createElement("button");
      el.type = "button";
      el.title = `Bus ${bus.vehicle.fleet_no}`;
      el.className =
        "h-3.5 w-3.5 rounded-full border-2 border-white shadow-md transition-transform hover:scale-125 focus:outline-none focus:ring-2 focus:ring-offset-1 focus:ring-amber-700";
      el.style.backgroundColor = STATUS_COLORS[bus.vehicle.status] ?? "#78716c";
      el.addEventListener("click", () => setSelected(bus));
      return new Marker({ element: el }).setLngLat([bus.lon, bus.lat]).addTo(map);
    });
    if (buses.length > 0) {
      const bounds = new maplibregl.LngLatBounds();
      buses.forEach((b) => bounds.extend([b.lon, b.lat]));
      if (!bounds.isEmpty()) map.fitBounds(bounds, { padding: 80, maxZoom: 13, duration: 0 });
    }
  }, [buses]);

  if (vehiclesQuery.isError) {
    return <ErrorState error={vehiclesQuery.error} onRetry={() => vehiclesQuery.refetch()} />;
  }

  const counts = buses.reduce<Record<string, number>>((acc, b) => {
    acc[b.vehicle.status] = (acc[b.vehicle.status] ?? 0) + 1;
    return acc;
  }, {});

  return (
    <div className="flex h-full flex-col">
      <PageHeader
        title="Live Fleet Map"
        description={`${buses.length} buses reporting — positions refresh every 10 seconds from telemetry.latest.`}
      />

      <div className="mb-4 flex flex-wrap gap-2">
        {Object.entries(STATUS_COLORS).map(([status, color]) => (
          <span
            key={status}
            className="inline-flex items-center gap-1.5 rounded-full bg-surface-raised px-2.5 py-1 text-xs text-stone-600 ring-1 ring-inset ring-stone-200"
          >
            <span className="h-2 w-2 rounded-full" style={{ backgroundColor: color }} />
            {status.replace(/_/g, " ")} ({counts[status] ?? 0})
          </span>
        ))}
      </div>

      <div className="relative min-h-[540px] flex-1 overflow-hidden rounded-xl border border-stone-200">
        {vehiclesQuery.isLoading ? (
          <div className="absolute inset-0 z-10 flex items-center justify-center bg-surface">
            <Spinner />
          </div>
        ) : null}
        <div ref={mapContainer} className="absolute inset-0" />
        {selected ? (
          <TwinPanel bus={selected} onClose={() => setSelected(null)} />
        ) : null}
      </div>
    </div>
  );
}

function TwinPanel({ bus, onClose }: { bus: BusOnMap; onClose: () => void }) {
  const twinQuery = useQuery({
    queryKey: ["twin", bus.vehicle.id],
    queryFn: () => getTwin(bus.vehicle.id),
    refetchInterval: 5_000,
    retry: 1,
  });
  const twin = twinQuery.data;

  return (
    <Card className="absolute right-4 top-4 z-20 w-80 max-w-[calc(100%-2rem)] shadow-lg">
      <CardHeader className="flex-row items-start justify-between">
        <div>
          <CardTitle>Bus {bus.vehicle.fleet_no}</CardTitle>
          <p className="mt-0.5 text-xs text-stone-500">{bus.vehicle.model}</p>
        </div>
        <div className="flex items-center gap-2">
          <StatusBadge status={bus.vehicle.status} />
          <button
            className="rounded-md p-1 text-stone-400 hover:bg-surface-sunken hover:text-stone-600"
            onClick={onClose}
            aria-label="Close twin panel"
          >
            <X className="h-4 w-4" />
          </button>
        </div>
      </CardHeader>
      <CardContent className="space-y-4">
        {twinQuery.isLoading ? (
          <Spinner className="py-6" />
        ) : twinQuery.isError || !twin ? (
          <p className="text-xs text-stone-500">
            Twin state unavailable — showing last telemetry sample.
          </p>
        ) : null}

        <div className="space-y-3">
          <Metric label="H2 level">
            <ProgressBar valuePct={twin?.h2_level_pct ?? bus.telemetry?.h2_level_pct ?? 0} />
            <span className="text-xs tabular-nums text-stone-500">
              {formatNumber(twin?.h2_level_pct ?? bus.telemetry?.h2_level_pct, 1)}%
            </span>
          </Metric>
          <Metric label="Battery SoC">
            <ProgressBar valuePct={twin?.battery_soc_pct ?? bus.telemetry?.battery_soc_pct ?? 0} tone="teal" />
            <span className="text-xs tabular-nums text-stone-500">
              {formatNumber(twin?.battery_soc_pct ?? bus.telemetry?.battery_soc_pct, 0)}%
            </span>
          </Metric>
        </div>

        <dl className="grid grid-cols-2 gap-x-4 gap-y-2 text-sm">
          <div>
            <dt className="text-xs text-stone-500">Speed</dt>
            <dd className="tabular-nums">{formatNumber(twin?.speed_kph ?? bus.telemetry?.speed_kph, 0)} km/h</dd>
          </div>
          <div>
            <dt className="text-xs text-stone-500">Fuel cell</dt>
            <dd className="tabular-nums">{formatNumber(twin?.fuel_cell_kw ?? bus.telemetry?.fuel_cell_kw, 1)} kW</dd>
          </div>
          <div>
            <dt className="text-xs text-stone-500">Odometer</dt>
            <dd className="tabular-nums">{formatNumber(twin?.odometer_km ?? bus.telemetry?.odometer_km, 0)} km</dd>
          </div>
          <div>
            <dt className="text-xs text-stone-500">Twin status</dt>
            <dd className="tabular-nums font-medium text-teal-700">{twin ? twin.status : "—"}</dd>
          </div>
        </dl>

        {twin?.route_id ? (
          <div className="space-y-1">
            <p className="text-xs font-medium text-stone-600">Assignment</p>
            <div className="flex flex-wrap gap-1.5">
              <Badge tone="teal">route {twin.route_id}</Badge>
            </div>
          </div>
        ) : null}

        <p className="text-[11px] text-stone-400">
          {twin
            ? `Twin updated ${formatDateTime(twin.updated_at)}`
            : bus.telemetry
              ? `Telemetry ${formatDateTime(bus.telemetry.ts)}`
              : "No recent samples"}
        </p>
      </CardContent>
    </Card>
  );
}

function Metric({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div>
      <div className="mb-1 flex items-center justify-between">
        <span className="text-xs font-medium text-stone-600">{label}</span>
      </div>
      <div className="flex items-center gap-2">{children}</div>
    </div>
  );
}
