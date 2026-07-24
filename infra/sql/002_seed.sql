-- =============================================================================
-- H2Fleet — 002_seed.sql
-- Seed data: 50 buses (H2-001..H2-050) on a mid-size city grid,
-- 3 refueling stations, all 20 feature toggles ON, sample incidents,
-- carbon credits and trades.
-- Coordinates: deterministic 10x5 grid centred on (13.4049, 52.5200) with a
-- small hash-based jitter so buses do not stack exactly on intersections.
-- =============================================================================

-- ------------------------------------------------------------- 20 toggles --
INSERT INTO public.feature_toggles (module, domain, enabled) VALUES
    ('telematics',              'fleet',    true),
    ('predictive-maintenance',  'fleet',    true),
    ('digital-twin',            'fleet',    true),
    ('fuel-monitoring',         'fleet',    true),
    ('route-energy-optimizer',  'fleet',    true),
    ('refueling-stations',      'infra',    true),
    ('leak-detection',          'infra',    true),
    ('dispatch-workforce',      'infra',    true),
    ('compliance-reporting',    'infra',    true),
    ('depot-management',        'infra',    true),
    ('passenger-pwa',           'citizen',  true),
    ('mobile-app',              'citizen',  true),
    ('demand-responsive',       'citizen',  true),
    ('carbon-credits',          'citizen',  true),
    ('open-data-portal',        'citizen',  true),
    ('fare-payments',           'commerce', true),
    ('loyalty-marketplace',     'commerce', true),
    ('energy-trading',          'commerce', true),
    ('gov-dashboard',           'commerce', true),
    ('advertising',             'commerce', true)
ON CONFLICT (module) DO NOTHING;

-- ---------------------------------------------------------------- 50 buses --
-- Grid: 10 columns (i % 10) x 5 rows (i / 10), ~450 m spacing, jittered.
INSERT INTO fleet.vehicles (fleet_no, vin, model, h2_capacity_kg, status, geom)
SELECT
    'H2-' || lpad(i::text, 3, '0'),
    'H2VIN' || lpad((i * 7919)::text, 14, '0'),
    (ARRAY['Caetano H2.City Gold', 'Solaris Urbino 12 hydrogen', 'Wrightbus StreetDeck Hydroliner'])[1 + (i % 3)],
    37.5,
    CASE WHEN i % 17 = 0 THEN 'maintenance'
         WHEN i % 11 = 0 THEN 'depot'
         ELSE 'active' END,
    ST_SetSRID(ST_MakePoint(
        13.4049 + ((i % 10) - 4.5) * 0.0065 + (sin(i * 12.9898) * 0.0008),
        52.5200 + ((i / 10) - 2.0) * 0.0042 + (cos(i * 78.2330) * 0.0005)
    ), 4326)
FROM generate_series(1, 50) AS i
ON CONFLICT (fleet_no) DO NOTHING;

-- -------------------------------------------------------------- 3 stations --
INSERT INTO infra.stations (name, capacity_kg, available_kg, status, geom) VALUES
    ('Depot Central HRS',    1200,  860, 'online',      ST_SetSRID(ST_MakePoint(13.4049, 52.5290), 4326)),
    ('Riverside HRS',         800,  415, 'online',      ST_SetSRID(ST_MakePoint(13.3720, 52.5085), 4326)),
    ('Northgate HRS',         800,  790, 'maintenance', ST_SetSRID(ST_MakePoint(13.4330, 52.5410), 4326))
ON CONFLICT DO NOTHING;

-- -------------------------------------------------------- sample incidents --
INSERT INTO infra.incidents (type, severity, bus_id, station_id, status, opened_at, meta)
SELECT 'leak', 'high', v.id, NULL, 'acknowledged', now() - interval '6 hours',
       '{"sensor": "h2-ppm", "reading_ppm": 8500, "location": "rear tank bay"}'::jsonb
FROM fleet.vehicles v WHERE v.fleet_no = 'H2-007'
ON CONFLICT DO NOTHING;

INSERT INTO infra.incidents (type, severity, bus_id, station_id, status, opened_at, meta)
SELECT 'fuel-cell-fault', 'medium', v.id, NULL, 'open', now() - interval '2 hours',
       '{"fuel_cell_kw_drop": 12.4, "code": "FC-UNDERVOLT"}'::jsonb
FROM fleet.vehicles v WHERE v.fleet_no = 'H2-023'
ON CONFLICT DO NOTHING;

INSERT INTO infra.incidents (type, severity, bus_id, station_id, status, opened_at, meta)
SELECT 'station-fault', 'low', NULL, s.id, 'resolved', now() - interval '2 days',
       '{"dispenser": 2, "issue": "nozzle seal replaced"}'::jsonb
FROM infra.stations s WHERE s.name = 'Northgate HRS'
ON CONFLICT DO NOTHING;

-- ------------------------------------------------------- sample telemetry ---
-- A few rows so dashboards/hypertable are non-empty on first boot.
INSERT INTO fleet.telemetry (bus_id, ts, speed_kph, h2_level_pct, fuel_cell_kw, battery_soc_pct, odometer_km, geom)
SELECT v.id,
       now() - (g || ' minutes')::interval,
       20 + 25 * abs(sin(g * 0.7)),
       40 + 55 * abs(sin(g * 0.13)),
       60 + 40 * abs(sin(g * 0.31)),
       55 + 35 * abs(cos(g * 0.21)),
       18000 + g * 0.4,
       v.geom
FROM fleet.vehicles v
CROSS JOIN generate_series(0, 30, 10) AS g
WHERE v.fleet_no IN ('H2-001', 'H2-002', 'H2-003')
ON CONFLICT DO NOTHING;

-- ----------------------------------------------------- sample carbon data --
INSERT INTO citizen.carbon_credits (period, kg_co2_avoided, credits, issued_at) VALUES
    ('2024-04', 41250.5, 4125.05, '2024-05-02T06:00:00Z'),
    ('2024-05', 44890.2, 4489.02, '2024-06-02T06:00:00Z'),
    ('2024-W23', 10980.7, 1098.07, '2024-06-10T06:00:00Z')
ON CONFLICT DO NOTHING;

-- ------------------------------------------------------ sample fare/trades --
INSERT INTO commerce.fare_payments (rider_sub, amount_minor, currency, mojaloop_transfer_id, status, created_at) VALUES
    ('citizen', 280, 'EUR', 'sim-trf-9f2c1a00', 'settled',   now() - interval '3 hours'),
    ('citizen', 560, 'EUR', 'sim-trf-9f2c1b41', 'settled',   now() - interval '1 day'),
    ('citizen', 280, 'EUR', NULL,               'initiated', now() - interval '15 minutes')
ON CONFLICT DO NOTHING;

INSERT INTO commerce.trades (kind, quantity_kg, price_minor, status, created_at) VALUES
    ('h2-sale',       250.0,  225000, 'settled',  now() - interval '5 days'),
    ('energy-export', 180.0,  148500, 'executed', now() - interval '1 day')
ON CONFLICT DO NOTHING;
