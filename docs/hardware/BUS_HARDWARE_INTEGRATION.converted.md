# H2Fleet — Bus Hardware Integration Guide

**Document:** BUS_HARDWARE_INTEGRATION.md
**Audience:** city fleet engineering & procurement (non-software)
**Scope:** per-bus hardware for the H2Fleet hydrogen-bus platform — sensing, acquisition, edge compute, uplink, power, security, installation, compliance, rollout and cost for a 50-bus fuel-cell bus fleet with a depot refueling station.
**Basis:** three engineering research briefs (R1 ready-made hardware, R2 open-hardware DIY, R3 protocols & safety, all dated 2026-07-26) reconciled against the shipped H2Fleet software contracts in this repository.
**Honesty convention:** prices are indicative classes where vendors do not publish list prices. Items the research could not confirm are flagged **[UNVERIFIED]** — those flags are deliberate and must survive into procurement decisions. Nothing in this guide substitutes for your homologation engineer, your H2 safety officer, or written OEM consent.

**Glossary for non-software readers:** *CAN / J1939 / FMS* — the vehicle's data networks; J1939 is the heavy-vehicle broadcast standard, FMS is the OEM-installed read-only gateway subset of it. *DBC* — the signal dictionary needed to decode a CAN bus. *TCU* — telematics control unit. *CVM* — cell-voltage monitoring of a fuel-cell stack. *PRD/PRV* — pressure relief device/valve on the H2 tanks. *LEL* — lower explosive limit (4 vol.% for hydrogen). *ATEX/IECEx, gas group IIC* — the EU/IEC equipment-certification regime for explosive atmospheres; IIC is the most demanding gas group and is the one hydrogen requires. *E-mark / ECE R10* — the vehicle EMC approval permanently installed electronics need. *TPM / secure element* — a chip that holds cryptographic keys so they cannot be copied off the device. *OTA* — over-the-air firmware update. *Edge unit* — the on-bus computer that runs the platform's agent. *Store-and-forward* — local buffering of data while the cellular link is down, replayed on reconnect.

---

## 1. Executive summary

The H2Fleet platform already ships its on-bus software: the **fluvio-edge** agent (`services/rust/fluvio-edge`), a small Linux workload (~1 CPU, low RAM) that consumes the on-bus Fluvio topic `bus-telemetry` and forwards batches to the platform Kafka topic `telemetry.raw` (lz4 compression, `acks=all`), with a CRC-framed, fsync'd store-and-forward spool that survives crashes and uplink outages. **The hardware question is therefore not "what software do we write" but "what do we run it on, and what do we wire into it."**

Two procurement paths are recommended, and they are not mutually exclusive:

**Path A — Ready-made (certified components).** Per bus: an automotive edge computer with dual CAN (Advantech TREK-674 class), a CAN/FMS front-end tracker (Teltonika FMC650), a 4G/Wi-Fi router (Teltonika RUTX11), CAN-native automotive H2 leak sensors for non-classified spaces (Nissha FIS FH2-HY06), ATEX/IECEx SIL2 detectors for the tank/PRD compartment (MSR PolyXeta PX2), H2-rated pressure transducers (WIKA MH-3-HY), and temperature probes.
- **Cost:** ≈ **€5,000–10,000 per bus** (indicative; several items quote-based [UNVERIFIED]), excluding the optional cell-voltage-monitoring (CVM) system, which alone is low-to-mid five figures per bus [UNVERIFIED] and is **not recommended at fleet scale** (see §3.3).
- **Fleet (50 buses):** ≈ **€250k–500k** hardware, plus installation labor and one-off costs (§7).
- **Why:** every box carries E-mark/CE/ATEX paperwork; minimal homologation exposure; fastest defensible path to permanent installation.

**Path B — Open-hardware DIY (pilot-first).** Per bus: a Seeed reComputer R1025-10 industrial Raspberry Pi CM4 gateway (TPM 2.0, watchdog, 3× isolated RS485, −30…+70 °C, ~$297) + supercap UPS + Copperhill PiCAN FD (CAN FD) + pre-calibrated Figaro CGM6812-B00 H2 module (trend/analytics channel, **not** a certified safety device) + u-blox GNSS + IMU + 4G/5G HAT, in an IP65 enclosure with Deutsch/M12 connectors.
- **Cost:** ≈ **$650–780 per bus** in parts for the core unit; ≈ **€1,200–2,300** adapted for H2Fleet with pressure sensing and dual isolated CAN (§5.B).
- **Fleet (50 buses):** ≈ **€60k–115k** hardware, **plus** one-off EMC pre-compliance lab time (€6–15k) and, for permanent series installation, full ECE R10 E-mark qualification (§6, §9).
- **Why:** 4–8× cheaper per bus, full control of the stack, native fit to the platform's TPM/signed-OTA security model — but it is **pilot-grade until EMC-qualified**, and its H2 sensing is an analytics channel only.

**Recommended strategy:** run **Path B on a 2-bus bench/pilot** immediately, procure **Path A for the 10-bus phase and fleet rollout**, and reuse everything learned (DBC decoding, alarm logic, installation patterns) across both. The certified H2 leak-alarm chain is identical in both paths: **certified detectors alarm locally, independent of connectivity; any retrofit unit only supervises them** (RS485/Modbus/relay contacts), never replaces them.

**Timeline (indicative planning figures, not from the research briefs):** bench & bench-top CAN reverse-engineering 4–6 weeks → 2-bus pilot 8–12 weeks → 10-bus phase ~3 months → full 50-bus rollout a further 6–9 months including homologation handling and works-council/GDPR process (§6). Total ≈ 12–18 months to full fleet.

**Three facts to internalize before spending anything:**
1. **There is no public standard for fuel-cell CAN signals.** No public DBC exists for Ballard FCmove, Toyota FC stack/HMU, or Cummins-Accelera modules (verified negative, 2026-07-26). Stack and tank internals require a vendor NDA/DBC, a vendor cloud API (e.g., Ballard FCServiceCloud), or a budgeted reverse-engineering campaign (§3.3, §9).
2. **Do not touch H2 plumbing.** UN ECE R134 type approval covers the certified H2 system; teeing into tank lines voids it. Use OEM signals or externally mounted sensors (§8).
3. **The telemetry unit must be read-only on vehicle CAN** — listen-only/silent mode plus galvanic isolation is a hard requirement, not a preference (§3.4, §8).

---

## 2. Target architecture

Two chains, deliberately separate: a **local safety chain** that works with the ignition on and the modem dead, and a **telemetry chain** that feeds the platform. They share sensors as *inputs* but the alarm path never depends on the telemetry path.

```
 ON-BUS (per vehicle)                                 DEPOT / CLOUD
 ┌─────────────────────────────────────────────────────────────────────┐
 │                                                                     │
 │  LOCAL SAFETY CHAIN (connectivity-independent, hardwired)           │
 │  ┌──────────────┐  ┌──────────────┐  ┌──────────────┐               │
 │  │ H2 detector  │  │ H2 detector  │  │ H2 detector  │  (certified:  │
 │  │ tank/PRD     │  │ FC enclosure │  │ cabin ceiling│  OEM + ATEX   │
 │  │ compartment  │  │              │  │ refuel port  │  retrofit)    │
 │  └──────┬───────┘  └──────┬───────┘  └──────┬───────┘               │
 │         │ relay/4-20mA/CAN │                │                       │
 │         ▼                  ▼                ▼                       │
 │  ┌───────────────────────────────────────────────┐                  │
 │  │ OEM safety ECU / certified alarm panel        │──► driver buzzer │
 │  │ warning ≥2% vol · auto tank shut-off ≥3% vol  │    + dash lamp   │
 │  │ detection <2 s · sensor-fault warning         │──► tank valves   │
 │  └───────────────┬───────────────────────────────┘   (OEM actuation│
 │                  │ RS485 Modbus / relay contacts      stays OEM's)  │
 │                  │ (SUPERVISION ONLY — read state, never actuate)   │
 │  TELEMETRY CHAIN │                                                  │
 │                  ▼                                                  │
 │  ┌──────────────────────────────────────────────────┐               │
 │  │ ACQUISITION LAYER                                │               │
 │  │  CAN ch1: FMS/J1939 powertrain (LISTEN-ONLY,     │               │
 │  │           galvanically isolated)                 │               │
 │  │  CAN ch2: FC/H2-system CAN (LISTEN-ONLY, NDA DBC │               │
 │  │           or passive logging)                    │               │
 │  │  RS485 Modbus: pressure/temp/H2 sensor telemetry │               │
 │  │  ADC/4-20mA: retrofit transducers · 1-Wire: temp │               │
 │  │  GNSS receiver · IMU                             │               │
 │  └───────────────────────┬──────────────────────────┘               │
 │                          ▼                                          │
 │  ┌──────────────────────────────────────────────────┐               │
 │  │ EDGE UNIT (the shipped workload)                 │               │
 │  │  on-bus publisher ─► Fluvio topic `bus-telemetry`│               │
 │  │  fluvio-edge agent: batches ─► Kafka producer    │               │
 │  │   (lz4, acks=all) · CRC+fsync store-and-forward  │               │
 │  │   spool on uplink loss · /healthz :8093          │               │
 │  │  local rules: dP/dt leak signature, geofence,    │               │
 │  │  harsh-event detection, refuel-event detection   │               │
 │  │  TPM/secure element: device key, mTLS, LUKS,     │               │
 │  │  signed A/B OTA (RAUC/Mender)                    │               │
 │  └───────┬─────────────────────────────┬────────────┘               │
 │          │ 4G/5G uplink                │ depot Wi-Fi offload        │
 └──────────┼─────────────────────────────┼────────────────────────────┘
            ▼                             ▼
     ┌─────────────────────────────────────────────┐
     │ APISIX gateway (mTLS, Keycloak JWT service  │
     │ identities) → Kafka backbone                │
     │                                             │
     │  telemetry.raw ─► telemetry-ingest ─►       │
     │      telemetry.enriched ─► digital-twin     │
     │  safety.leak.detected ─► incident pipeline  │
     │  fuel.reading ─► per-bus consumption model  │
     │  twin.updated ─► Redis hot twin (≥1 Hz H2)  │
     │  maintenance.predicted ─► ML consumers      │
     └─────────────────────────────────────────────┘
```

Design rules embodied in the diagram:

- **The OEM alarm chain stays the OEM's.** The platform receives leak state (listen-only CAN and/or RS485 supervision of certified detectors) and mirrors it as `safety.leak.detected` events. If supplementary retrofit sensors are added, their threshold logic runs on the edge unit (or a small MCU), not in the cloud — but they *add* alarming, they never become the sole alarm path (R3 §4.2, §5.3).
- **Store-and-forward is already engineered.** fluvio-edge's spool (64 MiB soft cap, fsync per batch, crash-tail truncation, offset checkpoint) covers tunnels and depot dead zones; hardware must give the agent a persistent `SPOOL_DIR` on power-fail-safe storage (eMMC/pSLC, never consumer SD cards) and a graceful shutdown signal (§3.8).
- **Safety decisions at full rate locally; decimated streams + event pushes uplink** (§4).

**What runs where (hardware view of the platform contracts):**

| Platform element | Location | Hardware implication |
|---|---|---|
| On-bus publisher → Fluvio `bus-telemetry` topic | edge unit, on-bus | acquisition daemons + Fluvio SC+SPU on the same box (platform `edge` compose profile) |
| `fluvio-edge` agent | edge unit, on-bus | ~1 CPU, low RAM; persistent eMMC spool dir; graceful SIGTERM on ignition-off |
| Uplink (mTLS via APISIX) | cellular/depot Wi-Fi | router or modem HAT + TPM-backed client cert |
| Kafka backbone + topics (`telemetry.raw`, `safety.leak.detected`, `fuel.reading`, `twin.updated`) | depot/cloud | none on-bus; sizing per §3.7 volumes |
| Redis hot twin (`twin.updated`), ML consumers (`maintenance.predicted`) | cloud | none on-bus — but they dictate the ≥1 Hz `h2_level_pct` and raw `h2_ppm` signal requirements that this hardware must deliver |
| Local safety chain (alarm + shut-off) | certified detectors + OEM safety ECU, on-bus | hardwired; must not depend on any of the above |

---

## 3. Subsystem-by-subsystem recommendations

Each subsection gives (a) ready-made options, (b) an open-hardware DIY build, and (c) our recommendation for this 50-bus fleet.

### 3.1 H2 leak detection & safety sensing

This is the subsystem where honesty matters most: **no DIY sensor is a certified life-safety device.** Figaro and its distributors state this verbatim for the TGS6812 family. The platform's `safety.leak.detected` events (fields: `incident_id, bus_id, station_id, sensor_id, h2_ppm, severity, location`) are an *information* feed; the authoritative alarm must be local, certified, and connectivity-independent.

**Placement (per R3 §2.2 — H2 is buoyant; sensors go high, at accumulation points):**

| Location | Why | Sensor class |
|---|---|---|
| Tank/cylinder compartment, high point + PRD vent path | Mandated by e.g. Hong Kong EMSD CoP §4.3.3; OEM ERGs place a sensor between tank and ceiling | ATEX Zone 1/2, **gas group IIC** — MSR PX2 / Dräger Polytron / H2scan 5330 IS class |
| Fuel-cell enclosure / stack compartment | Required sensor per CoP/ERG practice | ATEX-rated or OEM-integrated (Ballard FCmove integrates its own H2 sensor + ventilation) |
| Passenger cabin ceiling | H2 rises; standard practice | Automotive CAN-native (Nissha FH2-HY06) acceptable in non-classified space |
| Refueling receptacle/port area | Routinely-separated connection; required per first-responder guidance | ATEX-rated if in a classified pocket; otherwise CAN-native automotive |

**Sensor technology trade-offs (R3 §2.1) — why the shopping list looks like this:**

| Technology | Fit for 10–40% LEL alarms | Response | Weakness | Verdict on-board |
|---|---|---|---|---|
| Catalytic pellistor | Good (%LEL–100% LEL) | <5 s typ. | Poisoned by silicones/S-compounds; needs O2; responds to any combustible | Industry standard; watch contamination in cabin (sealants/cleaners) |
| MOS (metal-oxide) | Very sensitive at low ppm | Acceptable; humidity/temp-dependent | Poor selectivity (alcohols, CH4, CO); drift needs recalibration | Cheap; use in sensor-fusion/analytics, not alone |
| Electrochemical | Good low-concentration accuracy | Acceptable | Temp/humidity sensitive | Good for low-threshold early warning |
| Thermal conductivity (TCD) | Wide range to 100 vol%; poor low-end | Good | Poor selectivity (He, CH4 interfere) | Tank-line/exhaust/high-concentration points, not 0.4% alarms |
| Optical (TDLAS/Pd-film) | Good selectivity | Fast | Cost; limited market availability | Emerging premium option |

Market practice on new FCEV platforms: fuse electrochemical + MOS (+optical) elements into one safety ECU, 4–6 sensing points per vehicle, and specify the detection function at ISO 26262 ASIL-B/C (ASIL-C where detection triggers automatic H2 shut-off). A retrofit cannot redo the OEM's ASIL work — which is exactly why the certified detectors below alarm on their own and the telemetry layer only supervises.

**Alarm thresholds (design targets from R3 §2.3):** H2 flammability range 4–75 vol.% (LEL = 4%). The R134/GTR-13 strategy is summarized by h2tools.org as **~3% vol warning / 4% vol shutdown** (some OEMs stricter at 2%/3%); the Hong Kong CoP worked example is **>2.0% vol → visual+audible driver warning; >3.0% vol → automatic tank shut-off valves close; sensor fault (open/short) → warning; detection time < 2 s**. Fire-engineering practice commonly alarms at 1 vol.% (25% LFL). Recommendation: configure retrofit supervision to push `safety.leak.detected` with `severity=warning` at ≥2% vol (20,000 ppm) and `severity=critical` at ≥3% vol (30,000 ppm), with a 10 s pre/post burst of 10 Hz samples for the ML autoencoder — while the hardwired alarm chain acts on its own.

**(a) READY-MADE options:**

| Product | Principle | Range / response | Interfaces | Certs / rating | Price class | Integration notes |
|---|---|---|---|---|---|---|
| **Nissha FIS FH2-HY05 / FH2-HY06** | Catalytic combustion | 0–4 vol% (0–100% LEL); start-up ≤1 s, T90 ≤3 s | HY05: CAN std frame 500 kbps 12 V; **HY06: CAN ext frame 250/500 kbps 24 V** (HY04 PWM; HY11 analog 0.5–4.5 V) | Automotive; marketed for cars/buses/trucks | ~$300–800 **[UNVERIFIED]** | HY06 is the 24 V bus fit; drops directly onto the telemetry CAN channel — cabin ceiling + FC enclosure points |
| **neohysens NEO974/983/986 (+ NEO974A leak sensor)** | Thermal conductivity (works in low-O2) | 0–5 / 0–10 / 0–100 vol%; T90 <3–5 s; 100 ms update; 100 ppm resolution on CAN | CAN 2.0A/B (125–1000 kbit/s), Modbus RTU/RS485, 4–20 mA, 0–10 V; 12–32 VDC, <2.4 W; IP6K7 | Automotive −40…+85 °C; ATEX variants (e.g. NEO983HTA-ATEX) | ~€1–2.5k **[UNVERIFIED]** | TCD = true H2-selective; CAN + Modbus gives both telemetry and panel paths; good tank-area candidate where full Ex certification of the *installation* is engineered |
| **MSR-Electronic PolyXeta PX2 + SX1/SSAX1** | Catalytic / electrochemical ppm / MPS (15+ yr maintenance-free) | %LEL or ppm per cartridge | 4–20 mA, **RS-485 Modbus**, 2 relays (30 V/1 A); IP65/IP66 | **SIL2, ATEX/IECEx Zone 1 & 2 (Ex db / Ex ec), EN 60079-29-1** | ~€600–1.5k | Primary choice for the tank/PRD compartment; relays hardwire to the local alarm chain while the edge unit supervises via Modbus |
| **Dräger Polytron 5200/8200 CAT + Ex PR M DQ sensor** | Catalytic bead, double-detector, poison-resistant | DQ 0–100% LEL; LC 0–10% LEL; T90 ≤13–14 s | 4–20 mA (+HART), optional relays, remote head | **ATEX II 2G Ex db IIC T6/T5/T4**, IECEx, UL Cl. I Div 1; IP66 | ~$1.5–3.5k | IIC-rated — correct gas group for H2 classified pockets; premium alternative to PX2 |
| **Honeywell Sensepoint XCD (SPXCDALMG1M / SPXCDALMFX)** | EC (0–1000 ppm) or catalytic (0–100% LEL) per cartridge | per cartridge | 4–20 mA, **Modbus RTU**, 2 alarm + 1 fault relay; 24 VDC | ATEX/IECEx Zone 1, UL/CSA Cl. I Div 1; IP66 | ~$1,300–2,000 | EC cartridge suits early-warning low-ppm supervision at the refueling port |
| **H2scan HY-OPTIMA 5000 / 5330 IS Gen 5** | Pd-alloy solid state, H2-specific, auto-cal | in-line process ranges; no consumables; 10+ yr life | RS485 Modbus RTU (default), opt. 4–20 mA + 4 relays; 9–48 VDC | CE, UL; 5330 IS intrinsically safe; IP66/IP68 | ~$2–5k **[UNVERIFIED]** | Best lifetime economics; IS variant for classified zones |
| **Figaro TGS6812-D00 / CGM6812-B00 / FCS-H20** | Catalytic pellistor | 0–100% LEL; module 0–14,000 ppm, T90 ≤30 s | raw bridge / module analog 1–4.5 V | **FCS-H20 explicitly "FCEV-compliant", 15-yr life, siloxane-resistant** | sensor ~€65–80; module ~$50–150 | FCS-H20 is the OEM-grade element; CGM6812 module is the DIY-credible acquisition channel (below) |

**(b) OPEN-HARDWARE DIY build (analytics/supervision channel only):**
- **Figaro CGM6812-B00 / FCM6812** pre-calibrated module (0–14,000 ppm H2, 1–4.5 V analog out, 5 V, wire-break detection, ~$60–100) → **ADS1115 4-ch ADC breakout** ($16.66, I²C) on the edge unit. This is the raw per-sensor `h2_ppm` stream at 1–10 Hz that materially improves the platform's leak autoencoder (`maintenance.predicted`).
- Cheaper/looser alternatives from R2: raw TGS6812-D00 (€79.73, needs 3 V heater + differential readout, explicitly not certified), MQ-8 MOS module ($7.25 — trend/alarm-hint only, huge cross-sensitivity; say so in the pilot plan), MTCS2601 TCD (~$48, H2-selective principle but thin integration docs).
- **Supervision of certified detectors** (the compliance-critical function): the reComputer's 3× isolated RS485 ports run Modbus RTU to MSR/Honeywell-class detectors; a 4-ch opto-isolated DI board ($10–30) reads their alarm/fault relay dry contacts. The DIY unit thereby *reports* the certified chain's state and concentration, and never replaces it.
- Firmware: python-can / Modbus (pymodbus-class) acquisition publishing to the local Fluvio `bus-telemetry` topic; threshold logic local at 10 Hz.

**Depot refueling station note:** the platform's `safety.leak.detected` contract includes `station_id` — the same event pipeline is meant to carry depot-side detector events. Apply the identical rule at the depot: certified fixed detectors (MSR PX2 / Sensepoint XCD / Dräger Polytron class, ATEX IIC in classified zones, per ISO 19880 station context) alarm locally at the station; the platform ingests their state over Modbus/hardwired contacts via a depot gateway (a reComputer or the `edge` compose profile's Fluvio SC+SPU running at the depot covers this without new hardware classes).

**(c) RECOMMENDATION for this fleet:** Per bus: **1× MSR PolyXeta PX2 (ATEX Zone 1/2, SIL2, IIC-suitable via Ex db cartridge selection — confirm IIC rating per order)** in the tank/PRD compartment + **1× ATEX-rated point at the refueling port** (PX2 or Sensepoint XCD) + **2× Nissha FH2-HY06** (cabin ceiling, FC enclosure) on the telemetry CAN + **1× Figaro CGM6812-B00 analytics channel** into the edge unit. Relay outputs of the ATEX detectors hardwire to the local audible/visual alarm and interface to the OEM shut-off chain per the vehicle's existing safety design; the edge unit supervises everything over RS485/CAN. Rationale: the certified layer is defensible in an incident investigation; the Nissha/analytics layer delivers the 1 Hz per-sensor ppm the ML wants, at automotive cost. **Never** let a DIY sensor's output be the only thing between a leak and the driver.

### 3.2 Tank pressure/temperature monitoring

Bus H2 storage is typically **350 bar** (Solaris/Wrightbus/Van Hool class; Hyundai Elec City FC is 700 bar). The platform needs tank pressure at **1 Hz**: `h2_level_pct` (the ≥1 Hz signal the Redis hot twin's trend-based refueling detection consumes), `fuel.reading.consumption_kg_per_100km` / `estimated_range_km` derivation, dP/dt leak-signature watchdog, and the `maintenance.predicted` LSTM.

**(a) READY-MADE options:**

| Product | Specs | Certs / rating | Price class | Integration notes |
|---|---|---|---|---|
| **WIKA MH-3-HY** | Hydrogen-variant OEM mobile sensor, thin-film on welded SS, **H2-compatible wetted material**, analog 4–20 mA / voltage, diagnostics + signal-clamping options; designed for mobile FC applications incl. buses; −40…+125 °C class | mobile-hydraulic lineage; H2-material statement is the point of this SKU | ~€200–500 | The named safe H2 product; 4–20 mA → edge ADC with 250 Ω shunt, or via a Modbus I/O module |
| **WIKA MH-4-CAN / MHC-1** | Mobile-machinery pressure sensor, **CANopen / SAE J1939 output**, ranges to 1,000 bar, IP67/IP69K | **[UNVERIFIED for H2 service]** — H2 wetted materials must be confirmed per order | (quote) | Drops onto telemetry CAN directly *if* H2-rated wetted parts are confirmed in writing |
| **WIKA IS-3** | ATEX/IECEx/FM/CSA intrinsically-safe transmitter, ranges to 6,000 bar, 4–20 mA/HART | IS for Ex zones | (quote) | For sensors physically mounted in classified pockets |
| **Pt100/Pt1000 + transmitters** (e.g. WIKA TR10, Ex variants) | tank/line temperature | Ex variants available | low | 4–20 mA transmitters or 1-Wire/Modbus digital probes into the FMC650's 1-Wire/RS485 ports — cheapest retrofit path |

Procurement rule from R1 §3.1: for 350/700-bar H2 service require **H2-rated wetted materials (316L/Elgiloy) and a written H2-compatibility (embrittlement) statement**; generic hydraulic variants are **not** automatically H2-approved. Flag this on every RFQ.

**(b) OPEN-HARDWARE DIY build:** the same WIKA MH-3-HY transducers (sensing elements stay industrial — this is not where you save money), read via **ADS1115 + precision 250 Ω shunt** (4–20 mA → 1–5 V) or the **iQuest iQLF4A20M8C 8-ch current-loop HAT (~$42)** on the reComputer; temperature via 1-Wire probes. **Do not tee into H2 plumbing** (§8): use existing OEM sensor tap points, OEM signals read off the bus, or externally mounted surface-temperature sensing. A dP/dt leak-signature watchdog runs locally on the edge unit.

**(c) RECOMMENDATION:** 2–5× **WIKA MH-3-HY** per bus (tank bank + line, count per vehicle layout) + Pt100/1-Wire temperature per tank bank, at 1 Hz, decimating to 0.2 Hz when parked. Where the OEM HMU already broadcasts per-tank pressure/temperature on CAN (Hyundai/Toyota-class architectures expose this via service tooling), prefer a licensed DBC read over parallel hardware — but do not let DBC negotiations delay the pressure hardware, since `h2_level_pct` at ≥1 Hz gates the twin's refueling detection.

### 3.3 Fuel-cell stack & powertrain telemetry

The platform field `fuel_cell_kw` and the `maintenance.predicted` LSTM want stack voltage, stack current, net power, coolant temps, purge events. **The honest reality (R3 §1.3, verified negative): there is no public PGN set and no public DBC for any FC module in this fleet's class** — Ballard FCmove (dominant in transit buses), Toyota FC stack/HMU, Cummins-Accelera. Signals live on proprietary CAN or behind the vendor's own cloud (Ballard's new FCmove-SC ships with the FCServiceCloud Customer Insight portal). comma.ai's opendbc supports the Toyota Mirai 2021 but contains **no hydrogen/fuel-cell DBC** (inspected 2026-07-26).

What integrators actually monitor on FC buses (R3 §1.3, from DOE fuel-cell bus maintenance material and fleet practice): stack voltage & current (polarization tracking for degradation), CVM min/mean cell voltage, coolant inlet/outlet temperature, cathode air pressure/flow & compressor speed, anode pressure, **H2 tank pressure & temperature per tank** (350 bar typical bus storage), tank valve states, PRD/PRV status, anode purge events/valve state, hydrogen concentration in exhaust, buffer-battery SOC/SOH, FC system hours, leak-sensor concentrations and states. Ballard's FCmove-HD datasheet lists "Control interface: CANbus" plus remote diagnostics; its nominal window (250–500 V, 20–250 A, 70 kW net, 8 barg H2 supply) tells you the FC module already measures everything on this list — the access problem is contractual, not physical.

Three access paths, in order of preference:
1. **NDA + DBC from the FC-module vendor/OEM.** Ballard, Toyota, Symbio/Forvia all document "remote diagnostics over CAN." For a 50-bus municipal fleet, route this via the bus OEM's fleet portal contract (Solaris eSConnect, Wrightbus WB UPTIME 365 are *bundled* — negotiate raw-data export, not just dashboards). Cost: contract/legal effort; budget line in §7.
2. **Vendor cloud API.** Ballard FCServiceCloud-class portals can feed the platform server-side (no on-bus hardware at all for these signals). Check export terms before assuming.
3. **Reverse-engineering campaign.** Log traffic during known maneuvers/refuels with SavvyCAN/Busmaster + a CANable/CANedge-class logger, correlate with service-tool PIDs. Budget weeks of engineering per vehicle model (§7); results are model-specific and must be re-validated after OEM firmware updates.

**(a) READY-MADE option — independent stack measurement:** **SMART TESTSOLUTIONS CVM G5/G6S (MCM-IntelliProbe)** cell-voltage monitoring: G5 10 ch/module cascadable to ~600, CAN/Modbus TCP/EtherCAT/PROFINET, −20…+105 °C; G6S 54 ch/module, **−40…+105 °C, ≥25,000 h design life**, ~75% lower cost/channel. Systems typically **low-to-mid five figures EUR per bus incl. cell contacting [UNVERIFIED]**. Also Weidmüller CVM Box (ATEX zones 1/2, stationary-oriented). Plus Hall-effect DC current transducers (LEM HASS/HO class) and isolated HV voltage transducers feeding TCU analog inputs **[generic recommendation — specific H2-bus COTS product not verified]**.

**(b) OPEN-HARDWARE DIY build:** no credible DIY CVM; instead: Hall current transducer + isolated HV divider → ADS1115/iQuest 4–20 mA HAT for **stack gross V/I** (not per-cell), logged at 5–10 Hz locally, uplinked as 1 Hz min/mean/max. This yields polarization-trend analytics without touching cell wiring.

**(c) RECOMMENDATION:** Do **not** buy CVM systems fleet-wide. The OEM FC controller already computes stack health; a retrofit CVM duplicates it at five-figure cost. Instead: (1) pursue NDA/DBC and OEM-portal export as a procurement workstream starting *now* (it has the longest lead time); (2) fit **one pilot bus** with the DIY gross V/I channel to validate the LSTM's data diet; (3) treat CVM as a fallback for fleets with degraded OEM access — notably any legacy **Van Hool A330 FC** units, where the OEM's 2024 bankruptcy makes vendor telemetry continuity uncertain and independent measurement genuinely justified (§9).

### 3.4 CAN/J1939 integration

**Architecture: two channels.** Channel 1 = FMS gateway / powertrain J1939 (250 or 500 kbit/s, 29-bit IDs, broadcast PGNs — read passively). Channel 2 = FC/H2-system CAN (proprietary, possibly CAN FD on newer ECUs). One channel **galvanically isolated minimum**; both channels **listen-only/silent mode**.

**HARD REQUIREMENTS (from R3 §5.1, non-negotiable):**
- **Silent/listen-only mode**: no ACK, no transmit, no error frames — the node is electrically invisible. Configure in software *and* hardware-enforce (TX-disable jumper, Campbell SDM-CAN style; or contactless readers, FMSCrocodile class).
- **Galvanic isolation** between logger and vehicle bus (signal isolation — protects both sides from ground loops; it is not a safety barrier).
- **No request frames**: DM2/service data need PGN 59904 requests — that is transmission. Strict read-only mode = broadcast traffic only (DM1 65226, CCVS 65265, VD 65248, VDHR 65217, HOURS 65253, ET1 65262, TCO1 65132 fallback).
- **FMS gateway preferred where fitted**: it exists precisely so third parties never touch internal networks; it will not forward non-FMS requests and does not re-broadcast vehicle diagnostics. Caveat: FMS v05 self-declares diesel design — **no H2/FC signals**; whether FCEV bus OEMs populate FMS on FC buses is **[UNVERIFIED per model]**. PGN corrections from R3: vehicle speed is **65265 (CCVS)**, VIN is **65260** (not 65259).

**(a) READY-MADE options:**

| Product | CAN capability | Notes | Price class |
|---|---|---|---|
| **Teltonika FMC650** | 2× CAN (J1939/FMS), J1708, K-Line tacho | E-mark; also 2× RS232 + RS485, 4 DIN/4 DOUT/4 AIN, 1-Wire — doubles as the sensor aggregator; verified FMS/J1939 parameter decoding | ~€130–180 |
| **Advantech TREK-674** | 2× CAN 2.0 (J1939, OBD-II/ISO 15765) + J1708 | Full Linux edge box, ISO 7637-2 power (§3.6) | ~€1–2k |
| **NetModule NB3800** | CAN via module slot; also IBIS, RS-232I/RS-485I, DIO | EN 50155 rail-grade, galvanic isolation on power, ECE R10; ITxPT support for bus bodies | ~€1.5–3k |
| **Queclink GV600MG-class** | J1939 heavy-vehicle tracker | Low-cost FMS path; exact model/certs to confirm with distributor | ~$100–200 |
| **Kvaser / HMS Ixxat class CAN FD interfaces** | CAN **FD** capture | R1 gap: most fleet trackers are classic CAN only; pair with any of the above where FC ECUs use FD | (not deeply researched) |

**(b) OPEN-HARDWARE DIY build:** **Copperhill PiCAN FD** HAT (MCP2517FD + MCP2562FD, RTC, $49.95) for channel 2 (FD-capable); for the hardened build the **PiCAN FD Duo Isolated** ($179.95) gives dual galvanically isolated CAN FD in one HAT. Software stack is all mainline open source: **SocketCAN** (`ip link set can0 up type can bitrate 500000`), **can-utils**, **python-can** (+ python-can-j1939), **cantools** (DBC/KCD/ARXML decode), **Open-SAE-J1939** / **AgIsoStack++**, ISO-TP mainline since kernel 5.10. Bench adapters: CANable 2.0 (~$36) / DSD SH-C31A (~$59) — development only, not vehicle-fit. Note R2's caution: SPI MCP2518FD at 8 MHz is fine for 250–500 kbit/s logging; bump `txqueuelen` for ISO-TP. Avoid ESP32-S3 TWAI for anything beyond bench work (documented driver errata, espressif issue #12074).

**(c) RECOMMENDATION:** Channel 1 via the **FMC650** on the FMS connector (warranty-safest, E-marked, and its RS485/1-Wire/AIN ports aggregate §3.2 sensors) feeding the edge unit over Ethernet/RS232; channel 2 via the edge unit's own CAN FD interface in software-enforced listen-only with an isolated transceiver front-end. Every installation dossier documents silent mode + isolation + no-transmit-path — this is the warranty and insurance position (R3 §5.2: a passive tap is the strongest Magnuson-Moss/goodwill argument; get written OEM consent anyway).

---

### 3.5 Positioning & motion

The platform contract needs `lat`, `lon` on `telemetry.raw` at 1 Hz; geofencing runs locally on the edge unit; IMU adds harsh-driving/tilt/parking-impact events (also relevant to R134's crash/tilt shut-off context).

**(a) READY-MADE options:** GNSS is bundled in all recommended TCUs — FMC650 (Airoha AG3335MB, GPS/GLONASS/Galileo/BeiDou/QZSS, dual-band L1+L5, <2.5 m CEP, −165 dBm), TREK-674 (GPS/Glonass/BeiDou), RUTX11, NB3800 (CEP <2 m). 3-axis accelerometer in CalAmp LMU-5530 class trackers; FMC650 class units report crash/harsh events via their I/O.

**(b) OPEN-HARDWARE DIY build:** **u-blox NEO-M9N** (SparkFun SMA breakout, $76.50, 1.5 m) as the fleet default; **ZED-F9P RTK** ($259.95 / kit $315.95, 10 mm) only where depot lane-level positioning matters (hardened-pilot build). IMU: **Adafruit BNO085** ($24.95, on-chip fusion) or ICM-20948 ($14.95). Avoid the $15 NEO-M8N clones for fleet use (no L2).

**(c) RECOMMENDATION:** NEO-M9N class (or the TCU's built-in GNSS on Path A) at 1 Hz, roof antenna with ground plane; BNO085-class IMU for event detection. GNSS traces are **personal data** when linkable to a driver — tag the GDPR data domain at acquisition (§6 works-council note, R3 §5.4).

### 3.6 Edge compute unit

**The shipped workload is fixed:** `services/rust/fluvio-edge` — consumes the local Fluvio `bus-telemetry` topic, forwards batches to Kafka `telemetry.raw` (lz4, `acks=all`, batch 500 / 500 ms linger), CRC-framed fsync spool (64 MiB soft cap) with offset checkpoint, axum `/healthz` on :8093, SIGTERM graceful stop. Requirements: Linux, ~1 CPU, low RAM, a few hundred MB storage plus spool headroom, and a local Fluvio SC+SPU (the platform's `edge` compose profile runs this on-bus or at the depot). Any computer below can host it; the differences are power, temperature, CAN, TPM, and certification.

**(a) READY-MADE options:**

| Product | Compute | Vehicle fit | Certs | Price class | Integration notes |
|---|---|---|---|---|---|
| **Advantech TREK-674** | Atom E3827 dual-core 1.75 GHz, ≤4 GB, fanless | 9–32 VDC, **ISO 7637-2 / SAE J1113**, ignition on/off/delay power mgmt, MIL-STD-810G, −30…+70 °C | (vehicle-grade; request certs) | ~€1–2k | Best single-box Path A host: 2× CAN + WWAN + GNSS; runs fluvio-edge under its Linux; **[newer TREK-60N generation exists — check lineup]** |
| **NetModule NB3800** | 1.3 GHz dual-core, 1 GB, opt. SSD | 24/36/48 or 72–110 VDC, galvanic isolation, EN 50155 S2/C1, −40…+70 °C | **EN 50155, EN 45545-2, ECE R10, ECE R118 (on request)** | ~€1.5–3k | The homologation-strongest option; LXC containers host the agent; CAN/IBIS via module slots |
| **CalAmp LMU-5530** | Cortex A9 800 MHz, ≤512 MB, OpenWrt | internal backup battery, 3-axis accel | **[E-mark/CAN version per variant — verify]** | ~$300–600 | Legacy product; check current availability |
| **Teltonika RUTX11** (router role) | Cortex A7 quad, 256 MB, RutOS (OpenWrt) | 9–50 VDC, −40…+75 °C, DIN-rail | **CE/RED, ECE R10, EN 45545-2** | ~€300–400 | Not the agent host — the bus LAN/uplink router pairing (§3.7) |
| **Semtech AirLink MP70 / XR80** | router | 7–36 VDC, ignition sense | E-mark/PTCRB/FCC family | ~$1,099 / $1,259–1,799 | Premium carrier-certified uplink; no native CAN |

**(b) OPEN-HARDWARE DIY build:** **Seeed reComputer R1025-10** ($297 / €239 ex-VAT): CM4 4 GB/32 GB eMMC, **TPM 2.0, hardware watchdog, 3× isolated RS485, 12–24 V in, fanless, −30…+70 °C**, optional **SuperCAP UPS €22.79** — one purchase covers compute + secure identity + isolated sensor bus, and it comfortably runs fluvio-edge plus the acquisition daemons. Alternatives from R2: **Kunbus RevPi Connect 5** (~€593–735, CM5, DIN-rail native, 24 V — the hardened-pilot host), **OnLogic Factor 201/202** (from $333, TPM option), EDATEC ED-CM4IND (~€200–250, **8–36 V input** — widest stock range), Techbase ModBerry 500 CM5 (~€300+, native CAN option, login-gated pricing), BeagleBone Black/AI-64 (native CAN, industrial temp variant). Storage pattern (copy this): eMMC pSLC not SD, overlayfs/read-only root, hardware watchdog, RTC, supercap for graceful ignition-off shutdown (OVMS and AutoPi both implement the "power-manager MCU holds the rail N seconds then signals shutdown" pattern — AutoPi uses an RP2040 for exactly this; that MCU's shutdown signal is what triggers fluvio-edge's SIGTERM path so the spool checkpoint lands cleanly).

**(c) RECOMMENDATION:** Path A: **TREK-674** (or NB3800 where the operator wants EN 50155/ECE R118 paperwork, e.g. mixed tram-bus authorities) — plus RUTX11 for Wi-Fi/LAN. Path B: **reComputer R1025-10 + SuperCAP**, migrating to **RevPi Connect 5** for the hardened pilot. Either way: TPM-backed identity (§3.9), read-only rootfs, watchdog, and the spool directory on eMMC.

### 3.7 Connectivity & uplink

**Data volumes (R3 §3.2):** ≈ **54 MB/bus/day** uncompressed across all streams (14 h service, ~80–160 B payloads + ~80 B envelope), ≈ **2.7 GB/day / 81 GB/month** for 50 buses; with CBOR+lz4-class compression and on-change/delta reporting (the fluvio-edge SmartModule downsample hook exists for exactly this), expect **~0.9–1 GB/day fleet**. Even uncompressed this fits comfortably on 4G Cat 1/4/6; **the binding constraint is broker message rate (≈8 msg/s/bus → ~400 msg/s fleet peak), not bandwidth.** 5G buys little for telemetry; its value is video/future services. R2 flags 5G module pricing (~$90–160 for SIM8262A/RM520N-GL) as **[UNVERIFIED — estimated]**.

| Stream | Uplink MB/bus/day |
|---|---|
| H2 leak sensors (heartbeat + event bursts) | ~1.0 |
| Tank P/T (×5 tanks @1 Hz) | ~12.1 |
| Stack V/I @1 Hz aggregate | ~9.1 |
| Coolant/air @1 Hz | ~8.1 |
| Battery SOC/SOH | ~1.6 |
| J1939 speed/distance @1 Hz | ~11.6 |
| GNSS @1 Hz | ~10.1 |
| Odometer/hours | ~0.1 |
| Events + keep-alives | ~0.2 |
| **Total per bus per day** | **≈ 54 MB** |

At the 64 MiB default spool cap, a fully offline bus at this rate buffers roughly a day of uncompressed traffic before backpressure — size cellular coverage expectations and depot offload accordingly, or raise `SPOOL_MAX_BYTES` on the edge unit's eMMC.

**(a) READY-MADE options:** **Teltonika RUTX11** (~€300–400, LTE Cat 6 300 Mbps CA, dual SIM, Wi-Fi 5 ac, E-mark, EN 45545-2) as the bus router — its Wi-Fi doubles as the **depot offload** path (spool drain over depot WLAN instead of cellular). Premium alternative: Semtech MP70/XR80 (carrier-certified incl. FirstNet; managed via ALMS). FMC650's own LTE Cat 1 can serve as a fallback telemetry path.

**(b) OPEN-HARDWARE DIY build:** **Waveshare PCIe→M.2 4G/5G HAT + SIM8262A-M2 or RM520N-GL + antennas (~$130–180)** on the reComputer; WireGuard/Tailscale for management; the uplink itself is fluvio-edge's Kafka producer over mTLS through the APISIX gateway. Depot offload: Wi-Fi client mode to the depot AP when in range — the spool drains FIFO automatically once Kafka is reachable, preserving original device timestamps.

**(c) RECOMMENDATION:** 4G Cat 6 (RUTX11 / SIM8262A) + depot Wi-Fi offload at the refueling station; dual SIM with a fleet data pool sized at ~2–3 GB/bus/month with headroom; treat 5G as optional future-proofing given price uncertainty. Alert on `spool_depth` growth in `/healthz` — a degraded uplink is a *normal* edge operating mode, not a device fault.

### 3.8 Power & installation

The 24 V bus rail will try to kill the unit (R2 §5, ISO 7637-2 / ISO 16750-2): **load dump pulse 5a = 174 V on 24 V systems (ISO 16750-2 now demands 10 pulses, 151–202 V)**. A data-line TVS cannot survive this; the front end needs kilowatt-class parts.

**(a) READY-MADE:** the recommended TCUs solve this in the box — TREK-674 (ISO 7637-2 compliant input, ignition on/off/delay management), NB3800 (galvanically isolated 24–110 V input), RUTX11 (9–50 V with surge/transient protection), FMC650 (10–30 V with overvoltage/reverse-polarity protection, deep sleep). Installation: DIN-rail in the bus electrical cabinet where possible; ignition-sense wiring per vendor manual.

**(b) OPEN-HARDWARE DIY build (front-end recipe — what OVMS/AutoPi/PiCAN3 all do):** reverse-polarity protection (P-MOSFET or ideal-diode controller) → **load-dump TVS SM8S33A/SM8S36CA (24 V; verify clamp Vc ≈53–58 V is below the DC-DC input rating — this back-check "is where most load-dump designs actually get decided")** → π-filter (LC/common-mode choke) → wide-input buck (ED-CM4IND 8–36 V board-level, Mean Well DDR-30 9–36 V DIN brick ~$25 for the hardened build, or XY3606 9–36 V→5 V ~$2–4 at pilot scale) → 5 V rail. **Ignition-sense input** (24 V-tolerant divider + optocoupler, ~$2) drives the "power down in N minutes" state machine with supercap cover — the SIGTERM that lets fluvio-edge fsync its spool and checkpoint its offset. Enclosure: **IP65 aluminium die-cast** (Hammond 1550Z/RND, $20–60) or DIN-rail box; connectors **Deutsch DT04-6P/12P** (power+CAN+ignition, ~$8–20/set), **M12 A-coded** RS485/sensors, **M12 D-coded** Ethernet, SMA/TNC antennas; twisted pair CANH/CANL, 120 Ω termination once per bus end.

**(c) RECOMMENDATION:** electrical cabinet DIN-rail mounting; fused tap at the vehicle's auxiliary distribution with the fuse value documented in the installation dossier; supercap-backed graceful shutdown verified on the bench (pull power mid-forwarding; confirm spool replays FIFO with original timestamps — this is a bench-test gate in §6, not a nice-to-have).

### 3.9 Device identity, security & OTA

The platform's security model is: Keycloak-issued JWT service identities, a secrets policy, a hash-chained audit log, and insider-threat mitigations. The bus hardware must plug into that story, not bypass it. Concretely (R3 §4.4–4.5, R2 §4):

- **Device identity in hardware:** TPM 2.0 / secure element holds the per-device private key; it never leaves the chip. reComputer R1025 and OnLogic Factor ship TPM 2.0 already ("the cheapest TPM is the one already soldered"); add-on options: **Microchip ATECC608A/B** ($0.90–1.00 bare; Adafruit breakout $4.95), **LetsTrust TPM** (Infineon SLB9670, $42.25), Zymbit Zymkey 4i (~$75, vendor-locked). CM4/CM5 have no on-die secure boot — a discrete SE/TPM is effectively mandatory on Pi for fleet identity. On Path A, **verify TPM/secure-element availability at purchase** [not confirmed for TREK-674/NB3800 in the research]; if absent, terminate mTLS at a companion element or accept software-keyed certs as a documented risk exception.
- **mTLS with per-device X.509** through the APISIX gateway, CA hierarchy offline-root → online-intermediate → device leaf (ECC P-256); the edge device's Keycloak service identity is bound to that certificate. **Rotation:** EST/SCEP-class automation, 1–2-year certs, expiry alerts at 30/14/7 days — an expired cert bricks connectivity; the edge health stream (§4) reports days-to-expiry. Revocation disables the identity in the registry. Device provisioning, cert issuance, and every OTA action land in the platform's hash-chained audit log — consistent with insider-threat mitigation (no engineer can silently re-key a bus).
- **Disk & boot:** LUKS full-disk encryption with the key sealed in the TPM, dm-verity read-only rootfs.
- **Signed A/B OTA:** **RAUC** (mandatory signing, A/B + casync delta) as default; **Mender** (turnkey fleet UI; deltas commercial-tier) or SWUpdate+hawkBit as alternatives; on Raspberry Pi A/B specifically: Rugix Ctrl ("tryboot"), Mender's RPi integration, or RAUC+U-Boot. Requirements from R3 §4.5: atomic dual-partition updates, watchdog-driven automatic rollback, staged canary rollout, depot-idle maintenance windows, delta+resume. **Never OTA-flash while the leak-monitoring path is required to be live** — the safety function must survive update (independent safety chain, per §2).
- **Uptane** (TUF-derived automotive OTA metadata framework) is the reference for rollback-attack protection if the fleet's threat model justifies it.

**(c) RECOMMENDATION:** TPM-backed device certs + RAUC A/B on both paths; canary group = the 2 pilot buses, then 10-bus ring, then fleet rings of ~10. This mirrors the platform's existing Keycloak/audit posture and gives procurement a one-paragraph security answer: *keys in hardware, everything signed, every action audited, safe rollback.*

---

## 4. Signal → source → sample rate → platform topic/field mapping

Platform topic contracts (exact, from the shipped services):
`telemetry.raw` / enriched: `bus_id, ts, speed_kph, h2_level_pct, fuel_cell_kw, battery_soc_pct, odometer_km, lat, lon` · `safety.leak.detected`: `incident_id, bus_id, station_id, sensor_id, h2_ppm, severity, location` · `fuel.reading`: `bus_id, ts, h2_level_pct, consumption_kg_per_100km, estimated_range_km` · `twin.updated`: `bus_id, ts, state` (Redis hot twin) · `maintenance.predicted` consumes component-health signals (leak autoencoder on `h2_ppm`; LSTM on telemetry trends).

Reconciliation notes: the nine `telemetry.raw` fields are the **minimum contract**; supplementary channels (tank P/T, coolant, purge, DTCs, device health) ride as additional attributes on the same records and are what `maintenance.predicted`'s models actually consume. Where the research brief used `twin.state`, the shipped topic is **`twin.updated`**. Safety decisions run locally at full rate; the uplink carries decimated streams + event pushes.

| # | Signal | Source (bus side) | Local sample | Uplink rate | Platform topic → field(s) | Notes |
|---|---|---|---|---|---|---|
| 1 | H2 leak concentration, per sensor (tank compartment high point, FC enclosure, cabin ceiling, refuel port) | Certified retrofit detectors (Modbus/CAN) + OEM leak status via listen-only CAN; CGM6812 analytics channel | 10 Hz | heartbeat 0.1 Hz; **event push ≥2% vol (`severity=warning`) / ≥3% vol (`severity=critical`)** + 10 s pre/post burst | `safety.leak.detected` → `incident_id, sensor_id, h2_ppm, severity, location`; raw ppm also → `maintenance.predicted` autoencoder | Local alarm + OEM tank shut-off independent of uplink; sensor-fault state also pushed; `station_id` binds depot-side detectors |
| 2 | H2 tank pressure, per tank | OEM HMU broadcast (NDA DBC) else WIKA MH-3-HY retrofit | 1 Hz | 1 Hz (0.2 Hz parked) | feeds `h2_level_pct` derivation; attr on `telemetry.raw`; `maintenance.predicted` LSTM | Local dP/dt watchdog → also feeds `safety.leak.detected` |
| 3 | H2 tank temperature, per tank | same as above / Pt100 + 1-Wire probes | 1 Hz | 1 Hz | attr; feeds `h2_level_pct` (real-gas calc) | Refuel heating profile; −40 °C lower bound |
| 4 | Tank valve / PRD / PRV status | OEM CAN (NDA DBC) or retrofit switch sensing | 1 Hz | on-change | `twin.updated` → `state` | Read-only mirror of OEM states |
| 5 | H2 storage level (computed mass %) | Edge computation: P, T per tank + tank volume (compressibility) | 1 Hz | ≥1 Hz (twin trend needs it) | `telemetry.raw` + `fuel.reading` → `h2_level_pct`; `twin.updated` → `state` | **≥1 Hz is a hard platform need** — trend-based refueling detection on the hot twin; validate vs refuel receipts |
| 6 | Stack voltage / current / FC net power | FC module CAN (Ballard/Cummins NDA DBC); DIY gross V/I fallback | 5–10 Hz | 1 Hz agg (min/mean/max) | `telemetry.raw` → `fuel_cell_kw`; raw V/I attrs → `maintenance.predicted` | Degradation/polarization analytics in cloud |
| 7 | Stack coolant in/out temp, cathode air pressure | FC module CAN; fallback J1939 ET1 65262 if OEM maps it | 1 Hz | 1 Hz | attrs on `telemetry.raw` | Over-temp watch local |
| 8 | Anode purge events / purge-valve state | FC module CAN (proprietary) | event | event push | attrs on `telemetry.raw` → `maintenance.predicted` | Purge-frequency anomaly = early fault indicator |
| 9 | Buffer battery SOC / SOH | BMS via OEM CAN | 1 Hz | 0.2 Hz | `telemetry.raw` → `battery_soc_pct`; `twin.updated` → `state` | |
| 10 | Vehicle speed | J1939 CCVS **65265** (FMS gateway preferred) | 10 Hz (native 100 ms) | 1 Hz | `telemetry.raw` → `speed_kph` | Fallback TCO1 65132; harsh-event detection local |
| 11 | Odometer (total distance) | J1939 VD 65248 / VDHR 65217 | 1 Hz | on-change / 1 min | `telemetry.raw` → `odometer_km`; `twin.updated` | |
| 12 | FC/engine operating hours | J1939 HOURS 65253 or FC module hours | 1/min | 1/min | `twin.updated` → `state` | Stack-hours drive replacement planning (~7,000 h trigger per fleet guides) |
| 13 | VIN / asset identity | J1939 VIN **65260** (once, at provisioning) | once | once | binds `bus_id` | Note: VIN = 65260 (not 65259) |
| 14 | Active DTCs | J1939 DM1 65226 (broadcast, read-only-safe) | event | event push | attrs on `telemetry.raw` | DM2 needs request frames → skip in strict listen-only |
| 15 | GNSS position/heading/speed | Retrofit GNSS (preferred over vehicle nav) | 1 Hz | 1 Hz | `telemetry.raw` → `lat, lon` | GDPR-domain tagged; geofence local |
| 16 | IMU harsh-driving / tilt / impact events | BNO085-class IMU | 25–50 Hz local | event push | attrs on `telemetry.raw`; local safety context (R134 crash/tilt shut-off correlation) | Event detection runs on the edge unit |
| 17 | Refuel events (receptacle proximity + h2_level_pct step + dP/dt signature) | Composite edge rule; twin trend detection server-side | event | event push | `fuel.reading` → `ts, h2_level_pct`; consumption model updates `consumption_kg_per_100km, estimated_range_km` | Reconciles H2 mass balance; platform learns per-bus consumption |
| 18 | Edge device health (spool depth, cert days-to-expiry, GNSS fix, modem RSSI, forwarded/spooled totals) | fluvio-edge `/healthz` :8093 + acquisition daemons | 1/min | 1/min | `twin.updated` → `state` | Cert-expiry telemetry feeds the rotation workflow (§3.9); alert on spool growth |

---

## 5. Two complete per-bus builds

Price classes are indicative single-unit figures from the research briefs (mid-2026 street prices where cited); **[UNVERIFIED]** items are quote-based. Installation labor, harness fabrication, and depot-side works are excluded here and carried in §7.

### Build A — Ready-made (certified path)

| Item | Product | Qty | Unit price class | Line class | Role |
|---|---|---|---|---|---|
| Edge computer | Advantech TREK-674 (2× CAN, ISO 7637-2, ignition mgmt) | 1 | ~€1,000–2,000 | ~€1–2k | runs fluvio-edge; FC-CAN channel |
| Bus router / uplink | Teltonika RUTX11 (LTE Cat 6, Wi-Fi, E-mark) | 1 | ~€300–400 | ~€300–400 | uplink + depot Wi-Fi offload |
| CAN/FMS front-end + sensor aggregator | Teltonika FMC650 (2× CAN J1939, RS485, 1-Wire, AIN, E-mark) | 1 | ~€130–180 | ~€130–180 | FMS powertrain data + Modbus/1-Wire sensors |
| H2 detector, tank/PRD compartment | MSR PolyXeta PX2 (SIL2, ATEX/IECEx Zone 1&2, Modbus + 2 relays) | 1 | ~€600–1,500 | ~€600–1,500 | certified alarm + supervision |
| H2 detector, refueling port | MSR PX2 or Honeywell Sensepoint XCD (ATEX Zone 1, Modbus + relays) | 1 | ~€600–2,000 | ~€600–2,000 | certified alarm + supervision |
| H2 detector, cabin ceiling + FC enclosure | Nissha FIS FH2-HY06 (CAN ext, 24 V, T90 ≤3 s) | 2 | ~$300–800 **[UNVERIFIED]** | ~$600–1,600 | automotive CAN-native ppm |
| Tank/line pressure | WIKA MH-3-HY (H2-rated wetted materials, 4–20 mA) | 2–5 | ~€200–500 | ~€400–2,500 | 1 Hz `h2_level_pct` derivation |
| Temperature | Pt100/1-Wire probes + transmitters (WIKA TR10 class for Ex) | 2–5 | low | ~€150–400 | via FMC650 1-Wire/RS485 |
| H2 analytics channel | Figaro CGM6812-B00 module + ADC input | 1 | ~$60–150 | ~$60–150 | raw ppm for ML autoencoder |
| Antennas, cabling, connectors, enclosure | GNSS/LTE/Wi-Fi antennas, Deutsch/M12, cable, glands | 1 set | — | ~€300–600 | installation materials |
| TPM/secure identity | TPM option on edge box **[verify at purchase]** or companion SE | 1 | — | ~€0–100 | §3.9 |
| **Total per bus** | | | | **≈ €5,200–10,900** | **say €5k–10k** |

Deliberately excluded: SMART CVM G6S stack cell-voltage monitoring (low-to-mid five figures per bus [UNVERIFIED]) — see §3.3(c). Optional later per-bus add where OEM access fails.

### Build B — Open-hardware (pilot-first path)

R2's "H2Fleet full edge unit" BOM, adapted for this platform (pressure sensing added, second isolated CAN for the hardened variant, certified-detector supervision made explicit):

| Item | Product | Qty | Unit price | Role |
|---|---|---|---|---|
| Compute + identity + sensor bus | Seeed reComputer R1025-10 (CM4 4/32 eMMC, TPM 2.0, watchdog, 3× isolated RS485, −30…+70 °C) | 1 | $297 | runs fluvio-edge + acquisition daemons |
| Graceful shutdown | Seeed SuperCAP Backup Power | 1 | €22.79 | ignition-off SIGTERM cover |
| CAN FD (FC/H2 bus, listen-only) | Copperhill PiCAN FD (MCP2517FD/MCP2562FD, RTC) | 1 | $49.95 | channel 2 |
| CAN ch2 isolation upgrade (recommended) | → swap to PiCAN FD Duo Isolated | (1) | +$130 | dual isolated CAN FD |
| H2 analytics module | Figaro CGM6812-B00 / FCM6812 (0–14,000 ppm, 1–4.5 V) | 1 | ~$80 | raw ppm (analytics only, not certified) |
| ADC | ADS1115 4-ch breakout | 1 | $16.66 | H2 module + 4–20 mA aux |
| 4–20 mA pressure channel | iQuest iQLF4A20M8C HAT (8-ch current loop, 250 Ω) | 1 | ~$42 | reads MH-3-HY transducers |
| Tank/line pressure transducers | WIKA MH-3-HY (kept industrial on purpose) | 2 | ~€200–500 ea | 1 Hz pressure |
| Alarm supervision | 4-ch opto-isolated DI board | 1 | $10–30 | relay contacts from certified H2 panel |
| GNSS | SparkFun u-blox NEO-M9N SMA | 1 | $76.50 | `lat, lon` @1 Hz |
| IMU | Adafruit BNO085 | 1 | $24.95 | harsh/tilt events |
| Uplink | Waveshare PCIe→M.2 4G/5G HAT + SIM8262A-M2 (or RM520N-GL) + antennas | 1 | ~$130–180 **[price UNVERIFIED]** | 4G/5G + depot Wi-Fi |
| Enclosure & connectors | IP65 box + Deutsch DT + M12 + glands | 1 | $60–80 | vehicle fit |
| Power front-end | SM8S33A/SM8S36CA TVS + ideal-diode + π-filter + ignition sense (~$15 parts) | 1 | ~$15 | ISO 16750-2 24 V survival |
| Software | RAUC or Mender + LUKS/TPM-sealed keys, SocketCAN, can-utils, python-can, Open-SAE-J1939, cantools, pymodbus-class acquisition | — | $0 | §3.4/§3.9 |
| **Total per bus** | | | | **≈ €1,200–2,300** (core without pressure pair ≈ $650–780) |

### Hardened-pilot option (Build B+)

For the 2-bus pilot that must look like a series product: **Kunbus RevPi Connect 5** ($689, DIN-rail, 24 V, industrial temp) + **PiCAN FD Duo Isolated** ($179.95) + **ZED-F9P RTK kit** ($315.95, depot lane accuracy) + **Mean Well DDR-30** DIN brick (~$25) + load-dump front-end (~$15) + LetsTrust TPM if needed ($42) + FCM6812 + Sequent 8-IO HAT (~$90) + IP65/M12/Deutsch/shielded harness (~$150).
**Hardware ≈ $1,400–1,600 per bus; plus a one-off budget line of 1–2 days EMC pre-compliance lab time (€3–10k/day class → €6–15k total)** — this is what converts "DIY" into "pilot-deployable". R2's caveat stands: pre-compliance is **not** E-mark; permanent fleet installation of DIY hardware needs full ECE R10 qualification (§6, §9).

---

## 6. Fleet rollout plan — 50 buses

Phase gates below are engineering recommendations; durations are indicative planning figures (not from the research briefs).

### Phase 0 — Bench (weeks 1–6)
- Build 1× Build B unit + 1× Build A kit on the bench; flash the fluvio-edge workload; connect to a CAN simulator replaying recorded J1939 traffic.
- **Gates:** (a) power-cycle torture — pull power mid-forwarding, verify spool FIFO replay with original timestamps and clean offset checkpoint; (b) silent-mode verification with a second CAN node (confirm zero ACK/transmit from the tap); (c) `/healthz` metrics visible in platform monitoring; (d) RAUC A/B update + forced-failure rollback; (e) TPM-sealed LUKS boot; (f) CGM6812 + Modbus supervision reading a calibrated gas source (bump-test rig, not vehicle).
- Start the long-lead procurement workstreams in parallel: **OEM/FC-vendor DBC NDA**, OEM portal data-export terms, detector quotes (Nissha/neohysens/MSR prices are quote-based [UNVERIFIED]), and the **works-council/DPIA process** (below).

### Phase 1 — 2-bus pilot (weeks 7–18)
- Install 1× Build A and 1× Build B+ (hardened pilot) on two buses of the *same model*; written OEM consent obtained first (R3 §5.2: EU warranty is contractual + goodwill — paper it).
- DIY unit runs as **temporary test equipment under the operator's engineering sign-off**, galvanically isolated and data-only (R2 §5 practice).
- **EMC pre-compliance** lab session (1–2 days, €6–15k) on the B+ unit before it rides permanently.
- Validate against the platform: `h2_level_pct` ≥1 Hz continuity (twin refueling detection fires on real refuels), `safety.leak.detected` event flow from a controlled detector test, consumption model first `consumption_kg_per_100km` values vs refuel receipts.
- Begin/continue CAN reverse-engineering on the FC bus if the NDA path stalls (SavvyCAN/Busmaster logs during known maneuvers/refuels).

### Phase 2 — 10-bus phase (months ~5–8)
- Install the *winning* build on 8 more buses (mixed models if the fleet is mixed). Lock the installation dossier: fuse values, silent-mode evidence, isolation evidence, antenna/grounding drawings.
- OTA canary ring = these 10 buses; exercise one full signed-OTA cycle with rollback drill.
- Decide the **E-mark strategy** with the homologation engineer: for Path A, collect vendor E-mark/ECE R10 certificates into the vehicle files; for any DIY-derived unit intended for permanent series fit, commission full **ECE R10 + ISO 16750** qualification on a production-intent carrier (R2 §5: CE self-declaration does **not** substitute for R10 in vehicles).

### Phase 3 — Full fleet (months ~8–18)
- Rolling installation, ~5–10 buses/week depending on workshop capacity; spares pool (§7); depot Wi-Fi offload live at the refueling station.
- Fleet rings for OTA (canary 2 → ring 10 → remainder); audit-log review of provisioning and key operations per the platform's insider-threat model.
- Homologation handling per vehicle file: R10 E-mark evidence for every permanently installed ESA; no modification to the R134-certified H2 system (§8).

### Phase-exit KPIs (go/no-go)

| Gate | Measurable exit criteria |
|---|---|
| Bench → pilot | Spool torture test passed (zero loss over ≥100 power cycles); silent-mode trace shows zero TX/ACK; OTA rollback drill passed; `telemetry.raw` records decode with all nine contract fields populated |
| 2-bus pilot → 10-bus | ≥95% daily `h2_level_pct` 1 Hz continuity over 4 weeks; twin refueling detection matches fuel receipts on ≥90% of refuels; `safety.leak.detected` end-to-end (detector → local alarm → platform event) demonstrated in a controlled detector test; EMC pre-compliance report on file (DIY); zero warranty objections from OEM |
| 10-bus → fleet | Installation time ≤ target workshop slot; OTA canary cycle clean on 10/10 units; spool drain via depot Wi-Fi verified; E-mark strategy signed by homologation engineer; DPIA + works agreement executed |

### Warranty / OEM engagement (per R1 §4)
- **Solaris Urbino hydrogen:** eSConnect is bundled — negotiate raw-data export terms at contract; third-party CAN loggers: check warranty terms first.
- **Wrightbus StreetDeck Hydroliner:** WB UPTIME 365 is OEM-curated; deep data likely gated — read-only FMS tap for platform fields + portal negotiation for FC data.
- **Van Hool A330 FC:** OEM bankrupt (2024-04-08; business to VDL Groep, A-range city-bus production discontinued). Telemetry continuity uncertain — retrofit is *most* justified here; warranty largely moot, but coordinate parts/service with VDL Parts Belgium.
- **Hyundai Elec City FC / Toyota Sora class:** primarily OEM-portal/dealer diagnostics; EU telematics specifics [UNVERIFIED].
- Regardless of model: read-only tap + written consent is the default posture; transmitting onto OEM CAN is out of scope.

### Works council / GDPR (per R3 §5.4) — start in Phase 0, it has calendar lead time
- GPS traces/speed/driving events are personal data when linkable to a driver; controller = transit operator, platform vendor = processor (DPA required).
- Consent is usually the wrong basis in employment; use legitimate interest/contract with a documented balancing test; **run a DPIA**; data minimization (no off-duty tracking); retention limits; driver access/erasure rights; pseudonymized driver linkage; separate vehicle-safety vs HR data domains.
- **Works-council co-determination is mandatory** for monitoring tech in DE/FR/NL — codify usage in a works agreement. German case law (VG Lüneburg, 2019-03-19) found continuous 150-day location retention inadmissible: set proportionate retention *before* the pilot collects real traces.

---

## 7. Cost summary (indicative ranges)

Per-bus hardware from §5 ×50, plus one-off costs. All figures indicative; quote-based items flagged. Labor rates are local — the installation-labor lines are planning allowances, not research figures **[INDICATIVE — not from research briefs]**.

| Cost element | Path A (ready-made) | Path B (open-hardware) |
|---|---|---|
| Per-bus hardware | €5,200–10,900 (§5.A) | €1,200–2,300 (§5.B) |
| Per-bus installation labor + harness [INDICATIVE] | €800–1,500 | €800–1,500 |
| **Fleet hardware ×50** | **€260k–545k** | **€60k–115k** |
| **Fleet installed ×50** | **€300k–620k** | **€100k–190k** |
| One-off: EMC pre-compliance (pilot) | — (vendor certs) | €6–15k |
| One-off: full ECE R10 / ISO 16750 qualification (if DIY units go permanent-series) | — | lab quote; expect mid five figures **[UNVERIFIED]** |
| One-off: DBC NDA / FC-signal access | legal/engineering effort; possible vendor fee [UNVERIFIED] | same |
| One-off: reverse-engineering campaign (if NDA fails), per bus model | weeks of engineering time [INDICATIVE: 4–10 engineer-weeks/model] | same |
| One-off: bench/pilot equipment (CAN sim, bump-test rig, CANable/CANedge-class loggers, SavvyCAN workstation) | €2–5k [INDICATIVE] | shared |
| Annual: spares pool (~10% of fleet hardware) | €26k–62k | €6k–19k |
| Annual: connectivity (~2–3 GB/bus/month fleet pool) | per carrier quote | per carrier quote |
| Optional: SMART CVM per bus (only if OEM stack access fails) | low-to-mid five figures/bus [UNVERIFIED] | same |

Reading: Path A costs ~3–5× Path B per bus but transfers EMC/certification risk to vendors; Path B's savings are real but conditional on the E-mark strategy and on accepting that its H2 channel is analytics-grade. The hybrid (B for pilot, A for series) lands near Path A's fleet total with Path B's learning curve.

---

## 8. Compliance & installation checklist

Sign-off items per bus; file in the vehicle dossier.

**H2 system integrity (UN ECE R134 / GTR 13)**
- [ ] **No teeing into H2 plumbing.** The certified H2 system is untouched: no added fittings on tank/line hardware without re-approval. Pressure data comes from OEM signals or OEM-existing ports; leak sensors mount externally at accumulation points.
- [ ] OEM shut-off behavior (crash deceleration ≥3.25 m/s², tilt >23°, leak-detected isolation) is the OEM's; nothing in the retrofit modifies, inhibits, or proxies it.
- [ ] Any interface between retrofit alarm relays and vehicle systems is reviewed by the H2 safety officer and the OEM.

**EMC / E-mark**
- [ ] Every permanently installed electronic sub-assembly has **ECE R10 E-mark** evidence in the vehicle file (RUTX11, FMC650, NB3800 carry E-mark/ECE R10; request certificates for TREK-class units).
- [ ] DIY/pilot units: temporary-test-equipment sign-off on file, or R10 qualification certificate before series fit. CE self-declaration is **not** a substitute.
- [ ] ISO 16750-2/-3 (electrical loads/mechanical) and ISO 7637-2 transient immunity evidence collected; load-dump front-end clamp check documented for DIY power stages.

**Hazardous areas (ATEX/IECEx 2014/34/EU, EN 60079)**
- [ ] Sensors in classified pockets (tank fittings/PRD vent adjacency typically Zone 1, surrounds Zone 2) are **IIC-rated** — hydrogen is gas group IIC, the most demanding (e.g., Dräger Ex db IIC T6/T5/T4, MSR PX2 Ex db, H2scan 5330 IS). Confirm the IIC marking per order, not just "ATEX".
- [ ] Detector performance per EN 60079-29-1; installation by Ex-competent personnel; zone drawings updated.

**CAN access**
- [ ] Listen-only/silent mode configured **and** hardware-enforced; galvanic isolation fitted; zero request frames (no PGN 59904) in strict mode; evidence captured in commissioning log.
- [ ] Written OEM/fleet consent on file; FMS gateway used where fitted.

**Certified-detector rule (life safety)**
- [ ] The authoritative leak alarm chain is certified equipment alarming **locally** (driver warning ≥2% vol class; automatic tank shut-off ≥3% vol class; detection <2 s; sensor-fault warning), independent of connectivity and of the telemetry unit.
- [ ] DIY/analytics channels (CGM6812, MQ-8, TGS6812 raw) **supervise** certified detectors via RS485/Modbus/relay contacts and feed `safety.leak.detected`/`maintenance.predicted` — they **never replace** certified detection, and this is stated in the pilot plan and the incident dossier.
- [ ] OTA never flashes the edge unit while the supervised alarm path is required live; alarm chain independent of the telemetry SoC.

**Power & installation**
- [ ] Fused 24 V tap, fuse value documented; ignition-sense wired; graceful-shutdown test passed (spool replay verified); IP65 where exposed; Deutsch/M12 connectors; twisted-pair CAN with correct termination; antenna ground planes.
- [ ] Sensors mounted high at accumulation points per placement table (§3.1); CFD or CoP-based placement rationale on file for any non-standard location.

---

## 9. Honest risk register

| # | Risk | Likelihood | Impact | Mitigation | Residual |
|---|---|---|---|---|---|
| 1 | **OEM CAN access friction / warranty.** Third-party devices on vehicle networks can create warranty disputes; EU treatment is contractual + goodwill, not a Magnuson-Moss-style statute | Medium | Medium | Read-only silent mode + isolation + no request frames; FMS gateway where fitted; **written OEM consent before any install**; document the no-transmit architecture for insurers | Low-Medium |
| 2 | **No public FC DBCs** (verified negative for Ballard FCmove, Toyota FC/HMU, Cummins-Accelera as of 2026-07-26). `fuel_cell_kw` and stack-health ML inputs depend on proprietary signals | High (certainty of the fact) | High | Three parallel paths from day one: NDA/DBC via OEM contract; vendor cloud API (FCServiceCloud class); budgeted RE campaign. DIY gross V/I fallback on pilot | Medium — schedule risk on `fuel_cell_kw` richness, not on core telemetry |
| 3 | **Uncertified DIY H2 sensing mistaken for safety equipment.** TGS6812/CGM6812/MQ-8 are explicitly not certified life-safety devices | Medium (organizational drift) | Severe | Certified-detector rule (§8) engineered and documented; DIY channels branded "analytics" in every doc; alarm chain hardwired and local; ASIL-B-target independence for any supplementary alarming | Low if governance holds |
| 4 | **E-mark/homologation cost for DIY units.** R10 qualification is real lab spend; pre-compliance (€6–15k) does not equal approval; radiated emissions from HDMI/USB3/5G cabling are the usual killers | Medium | Medium | Path A carries vendor E-marks; DIY runs as temporary test equipment in pilot; series decision only after pre-compliance data; shielded enclosures/filtered connectors designed-in from day one | Medium |
| 5 | **5G price/availability uncertainty.** Module prices (~$90–160) estimated, not confirmed on dated listings | Medium | Low | 4G Cat 6 is the baseline and sufficient for ~54 MB/bus/day; 5G optional/forward-fit | Low |
| 6 | **OEM telemetry continuity — the Van Hool lesson.** Van Hool's 2024 bankruptcy (business to VDL; A-range discontinued) strands OEM portals; any vendor-curated portal (eSConnect, WB UPTIME 365, FCServiceCloud) carries the same vendor-mortality risk | Low per vendor, severe when it hits | High | Independent retrofit sensing (this guide) keeps the platform's core fields vendor-independent; contract for data export/escrow where portals are used; CVM fallback for stack health on orphaned fleets | Low-Medium |
| 7 | **Quote-based pricing.** Nissha, neohysens, MSR, H2scan, SMART CVM prices are indicative classes [UNVERIFIED]; fleet totals could move ±30% | High | Medium | Competitive RFQs across the named alternatives (MSR/Honeywell/Dräger; Nissha/neohysens) before phase-2 commitment | Low |
| 8 | **FMS on FCEV unverified per model.** FMS self-declares diesel design; FC bus OEMs may not populate gateways | Medium | Low | Commissioning checklist verifies PGN 64977 capability report per bus; TCO1/proprietary fallbacks exist for speed/distance | Low |
| 9 | **Cert expiry bricks connectivity** (fleet-wide operational risk, self-inflicted) | Low (with automation) | Medium | EST/SCEP-class rotation, 30/14/7-day alerts, days-to-expiry in device-health stream (§4 row 18) | Low |
| 10 | **GDPR/works-council delay.** Location/driver data governance has calendar lead time and can block pilot data collection | Medium | Medium | Start DPIA + works agreement in Phase 0; proportionate retention (VG Lüneburg lesson); pseudonymized driver linkage | Low |
| 11 | **Uplink outages lose data (perceived).** Tunnels/depot dead zones | High (routine) | Low | fluvio-edge spool is engineered for exactly this (CRC frames, fsync, FIFO replay); depot Wi-Fi offload; alert on spool depth, not on "offline" | Low |

---

## 10. References

Consolidated from research briefs R1/R2/R3 (all 2026-07-26); access dates as noted there.

**Commercial TCUs / edge gateways (R1)**
- Teltonika FMC650 datasheet v2.4 — https://wiki.teltonika-gps.com/images/f/f5/Datasheet_FMC650.pdf ; wisp.net.au listing (2026-05-19); rinho.com.ar FMC650 comparison (2026-01-29)
- Teltonika RUTX11 — https://www.teltonika-networks.com/products/routers/rutx11 (2026-06-04); simbase.com review (2025-11-14); 4grouter.co.uk listing
- Semtech AirLink MP70/XR80 — https://www.industrialnetworking.com/airlink-mp70-pro-series-cellular-router/ (2026-07-26); modemexpress.com/mp70 (2026-04-26)
- CalAmp LMU-5530 datasheet — https://help.calamp.com/resources/hardware-spec-sheets/calamplmu-5530datasheet.pdf
- Advantech TREK-674 — https://www.industrysearch.com.au/compact-in-vehicle-computing-box-trek-674/p/153234 ; elmark.com.pl (2025-03-17)
- NetModule NB3800 — https://www.netmodule.com/en/products/router/nb3800-2l2wac-g ; sartelco.it NB3800 datasheet
- Queclink GV600MG — https://flespi.com/devices/queclink-gv600mg

**H2 safety sensors (R1)**
- Nissha FIS FH2 — https://connect.nissha.com/gassensor/en/product/hydrogendetector/ (2026-04-01)
- neohysens NEO9xx — https://www.gkzhan.com/chanpin/13565112.html (datasheet transcription, 2025-03-18)
- Figaro TGS6812-D00 — https://www.figarosensor.com/product/docs/TGS%206812D00%280720%29.pdf ; FCS-H20 via sensor-test.de (2026-06-11)
- MSR-Electronic PolyXeta — https://www.msr-electronic.de/wp-content/uploads/2025/12/MSR-Electronic-Produktserien-25-e.pdf
- Dräger Polytron / Ex sensors — https://www.draeger.com/en-us_us/Products/Catalytic-Bead-DraegerSensors (2026-07-17); Polytron SE Ex datasheet (II 2G Ex db eb IIC)
- Honeywell Sensepoint XCD — https://automation.honeywell.com/au/en/products/sensing-solutions/gas-and-flame-detection/fixed-gas-and-flame-detection/fixed-gas-detectors/sensepoint-xcd
- H2scan HY-OPTIMA — https://h2scan.com/product/hy-optima-5000/ (2026-03-11)

**Pressure / CVM (R1)**
- WIKA MH-3-HY — https://www.ics-schneider.de/wasserstoff/?lang=en (2024-04-02); MH-4-CAN/MHC-1 — ics-schneider.de mobile machinery (2025-12-11); IS-3 — ics-schneider.de/wasserstoff (2025-12-10)
- SMART TESTSOLUTIONS CVM G5/G6S — https://www.smart-testsolutions.de/en/company/press/release/smart-testsolutions-wins-another-order-for-a-series-cvm.html (2025-11-17); 14,000-modules milestone (2026-03-16); horiba-fuelcon.com SMART CVM

**OEM bus context (R1)**
- Solaris Urbino hydrogen — https://www.sustainable-bus.com/fuel-cell-bus/solaris-urbino-hydrogen-baviera/ (2022-04-20); Jaltest Solaris coverage — jaltest.com
- Wrightbus UPTIME 365 — https://wrightbus.com/en-gb/wrightbus-unveils-its-new-telemetry-system (2021-06-30)
- Van Hool / VDL — https://www.sustainable-bus.com/news/van-hool-vdl-crisis-future/ (2024)
- Hyundai Elec City FC — https://ecv.hyundai.com/global/en/products/elec-city-fuel-cell-fcev

**Open hardware & DIY (R2)**
- Seeed reComputer R1025-10 — seedstudio.com/reComputer-R1025-10-p-5895.html ; kiwi-electronics.com (2026-05-07, incl. SuperCAP €22.79)
- Kunbus RevPi Connect 5 — digikey.com product highlight (2025-08-21); phytools.com/products/revpi-connect-5
- OnLogic Factor — onlogic.com/store/fr201/ ; Techbase ModBerry — iiot-shop.com (login-gated); EDATEC ED-CM4IND — kamami.pl (2025-10-06); BeagleBone — beagleboard.org
- CANable 2.0 / candleLight — electronics-lab.com (2024-09-22); makerbase3d.com (2026-03-04); github.com/candle-usb/candleLight_fw; DSD SH-C31A — warehousesoverstock.com (2025-04-17)
- Copperhill PiCAN FD / FD Duo Isolated — copperhilltech.com (2026-04-02 / 2026-07-22); free ESP32 J1939 stack blog (2026-06-19); SK Pang PiCAN3 — skpang.co.uk; Waveshare RS485 CAN HAT / 2-CH CAN HAT — waveshare.com
- SocketCAN/ISO-TP — github.com/V0lk3n/can-isotp (2025-02-22); can-utils — github.com/linux-can/can-utils; python-can — github.com/hardbyte/python-can; cantools — github.com/cantools/cantools (accessed 2026-07); Open-SAE-J1939 / AgIsoStack++ — github.com/topics/j1939 (2026-06-09); CANopenNode — github.com/CANopenNode/CANopenNode
- Sensors (DIY) — Figaro CGM6812/FCM6812 (soselectronic/digiwarestore, 2026-03); MQ-8 — SparkFun ($7.25); MTCS2601 — Alibaba listing (2026-01-08); ADS1115 — electromaker ($16.66); iQuest iQLF4A20M8C — i-quest.co (2026-01-15); NEO-M9N/ZED-F9P — sparkfun.com (accessed 2026); BNO085/ICM-20948 — adafruit.com (2026-02-16)
- Identity/OTA — ATECC608 (Mouser/DigiKey 2026-03-04; adafruit.com/product/4314); LetsTrust TPM — thepihut.com/cytron.io; tpm2 Pi guide — ohyaan.github.io; Zymkey 4i — element14 (accessed 2026); RAUC — Pengutronix; Mender; SWUpdate/hawkBit; Rugix; OTA comparisons — rugix.org blog (2026-02-28), 32blog.com (2026-03-26), proteanos.com (2026-03-28); AutoPi (Tailscale, smart PSU) — autopi.io/hardware/compare (accessed 2026-07)
- Power/EMC — magnias.com ISO 7637-2 app note (2026-05-30); en.leiditech.com (2026-03-05); onsemi AND8228; XY3606 — electrobot.co.in; binder M12 — binder-connector.com (accessed 2026)
- Reference projects — OVMS v3 (openvehicles.com; github.com/openvehicles/Open-Vehicle-Monitoring-System-3; kit £235 shop.openenergymonitor.com); Macchina M2 — github.com/macchina/m2-hardware (CC BY-SA); CSS Electronics CANedge — csselectronics.com (2023-06-19); SavvyCAN — savvycan.com; Busmaster — github.com/rbei-etas/busmaster

**Protocols & safety (R3)**
- FMS-Standard v05 — https://www.fms-standard.com/Truck/down_load/fms%20document_v_05_vers.07.07.2024.pdf (2024-07-07); iWave FMS overview — iwave-global.com (2025-07-27); CANGO — cangomobility.com (2023-08-29); FMSCrocodile — copperhilltech.com (2026-02-10)
- J1939 fundamentals — imatrixsys.com (2026-03-16); voltage-j1939 PGN DB — github.com/EvanL1/voltage-j1939 (2025-12-19)
- Ballard FCmove-HD datasheet — hfcnexus.com; FCmove-SC launch — blog.ballard.com (2025-09-18)
- NHTSA TSB MC-10185416 (NEXO HMU per-tank P/T) — static.nhtsa.gov/odi/tsbs/2020/MC-10185416-0001.pdf
- opendbc (Mirai; no H2 DBC — inspected 2026-07-26) — github.com/commaai/opendbc
- H2 leak detection review — MDPI Energies 17(16):4059 (2024-08-15) — mdpi.com/1996-1073/17/16/4059
- h2tools.org mobile H2 detection FAQ — h2tools.org/faq/hydrogen-detection-methods-mobile-applications
- Hong Kong EMSD CoP for Hydrogen Fuelled Vehicles (Feb 2024) — cnsd.gov.hk
- HyResponder Lecture 7 placement — hyresponder.eu (2021); Hyundai/NEXO ERGs — energysecurityagency.com / h2tools.org; SFPE FPEET Issue 49 — sfpe.org
- SAE J2578-2014; SAE J2579; ISO 23273; ISO 26142; ISO 6469-2/-3
- ECE R134 retrofit context — ennovationtech.eu ADR blog (2025-06-16); ISO 14687:2025 — standards.iteh.ai (2025-02-12); ATEX zones — intrinsicallysafestore.com (2026-07)
- CAN silent mode — jcom1939.com (2025-08-17); Campbell SDM-CAN (TX/ACK disable) — s.campbellsci.com; Kvaser Memorator (isolation) — kvaser.com
- Edge/cloud architecture — iotdigitaltwinplm.com (2026-05-18); hivemq.com MQTT vs Kafka (2026-02-27); resolute-dynamics.com (2026-07-06); i-flow.io (2026-07-16); a-bots.com (2025-10-02); mitwireless.com store-and-forward (2025-08-28)
- X.509/TLS — oneuptime.com Azure guide (2026-02-16); fss.cc (2025-12-03); Microsoft Learn IoT Hub Q&A (2026-05-14)
- OTA/Uptane — patsnap OTA safety (2026-05-19); github.com/awwad/uptane; ACM Uptane analysis (2023)
- Functional safety & law — jamasoftware.com ASIL guide (2026-05-06); autocare.org Magnuson-Moss; queclink.com GDPR (2026-05-13); toolsense.io VG Lüneburg (2025-02-10); geotab.com driver privacy (2021-03-22); traknova.com GDPR (2025-07-16)
- DOE Fuel Cell Bus Maintenance Module 7 — energy.gov (fcm07r0.pdf); buscmms.com hydrogen bus maintenance (2026-05-28); indexbox.io H2 sensor market analyses (2026-05-07); bosch-mobility.com FC sensor portfolio (2026-07-14); marketsandmarkets.com H2 sensor market (2026-05-13)

**Platform (this repository)**
- `services/rust/fluvio-edge` — edge agent README/SPEC: `bus-telemetry` → `telemetry.raw` (lz4, acks=all), CRC+fsync spool, `/healthz` :8093
- `infra/fluvio-edge/docker-compose.edge.yml` — `edge` compose profile (Fluvio SC+SPU + agent + sample publisher)

---

*End of document. Verify all [UNVERIFIED] items and indicative price classes by RFQ before commitment; involve the homologation engineer, H2 safety officer, OEM, and works council at the phases marked in §6.*
