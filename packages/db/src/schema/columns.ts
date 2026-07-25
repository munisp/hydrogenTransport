// Shared column helpers for the H2Fleet Drizzle schema.
import { customType } from "drizzle-orm/pg-core";

// PostGIS geometry(Point, 4326) — exposed as WKT/EWKT text to TS callers
// (ST_AsText-style); spatial indexes live in the goose migrations.
export const geometryPoint = customType<{ data: string; driverData: string }>({
  dataType() {
    return "geometry(Point, 4326)";
  },
});

// PostGIS geometry(Polygon, 4326) — depot/geofence zones.
export const geometryPolygon = customType<{ data: string; driverData: string }>({
  dataType() {
    return "geometry(Polygon, 4326)";
  },
});

// PostGIS geometry(LineString, 4326) — route corridors.
export const geometryLineString = customType<{ data: string; driverData: string }>({
  dataType() {
    return "geometry(LineString, 4326)";
  },
});
