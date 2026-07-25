import type { LucideIcon } from "lucide-react";
import {
  Building2,
  Bus,
  Database,
  Fuel,
  Landmark,
  Megaphone,
  UserRound,
} from "lucide-react";
import type { OnboardingPersona } from "../../api/admin";

/** Declarative persona configuration driving the /welcome onboarding wizard. */

export interface PersonaField {
  key: string;
  label: string;
  type?: "text" | "email" | "password";
  placeholder?: string;
  required?: boolean;
  hint?: string;
}

export interface PersonaDef {
  id: OnboardingPersona;
  label: string;
  tagline: string;
  icon: LucideIcon;
  /** Citizen accounts are provisioned instantly; all others queue for approval. */
  instant: boolean;
  /** Extra fields appended to step 1 (contact details). */
  detailsFields: PersonaField[];
  /** Step 2 fields beyond the shared "organisation" input (stored in meta). */
  credentialFields: PersonaField[];
  /** One-line summary of what happens after submitting. */
  outcome: string;
}

export const PERSONAS: PersonaDef[] = [
  {
    id: "citizen",
    label: "Citizen",
    tagline: "Arrivals, DRT shuttles, carbon credits and service alerts.",
    icon: UserRound,
    instant: true,
    detailsFields: [
      {
        key: "password",
        label: "Password",
        type: "password",
        required: true,
        hint: "At least 8 characters. You can change it after the first sign-in.",
      },
    ],
    credentialFields: [],
    outcome: "Your account is created instantly — no approval needed.",
  },
  {
    id: "driver",
    label: "Driver",
    tagline: "Assigned jobs, shift dispatch and one-tap incident reporting.",
    icon: Bus,
    instant: false,
    detailsFields: [],
    credentialFields: [
      {
        key: "license_number",
        label: "Driver licence number",
        required: true,
        placeholder: "e.g. D-4471-90210",
      },
      {
        key: "h2_certification",
        label: "H2 vehicle certification",
        placeholder: "Certificate id (if held)",
        hint: "Operators verify this before assigning hydrogen vehicles.",
      },
    ],
    outcome: "An operator reviews your credentials before the account activates.",
  },
  {
    id: "operator",
    label: "Operator",
    tagline: "Fleet, infrastructure and workforce operations consoles.",
    icon: Building2,
    instant: false,
    detailsFields: [],
    credentialFields: [
      {
        key: "team",
        label: "Team / function",
        required: true,
        placeholder: "e.g. Dispatch, Maintenance, NOC",
      },
    ],
    outcome: "A platform administrator approves operator access.",
  },
  {
    id: "station-staff",
    label: "Station staff",
    tagline: "Refueling station status, queue management and safety workflow.",
    icon: Fuel,
    instant: false,
    detailsFields: [],
    credentialFields: [
      {
        key: "station",
        label: "Primary station",
        required: true,
        placeholder: "e.g. Depot North HRS-2",
      },
      {
        key: "safety_training",
        label: "Safety training completed",
        placeholder: "e.g. H2 handling level 2, 2024",
      },
    ],
    outcome: "An operator verifies your station assignment before activation.",
  },
  {
    id: "advertiser",
    label: "Advertiser",
    tagline: "On-bus and digital ad inventory, campaigns and reach reports.",
    icon: Megaphone,
    instant: false,
    detailsFields: [],
    credentialFields: [
      {
        key: "company_reg",
        label: "Company registration no.",
        required: true,
        placeholder: "e.g. 556-123-4567",
      },
      {
        key: "campaign_interest",
        label: "Campaign interest",
        placeholder: "e.g. Interior cards, depot screens",
      },
    ],
    outcome: "The commerce team approves advertiser accounts after review.",
  },
  {
    id: "data-partner",
    label: "Data partner",
    tagline: "Open datasets, GTFS feeds and research data access.",
    icon: Database,
    instant: false,
    detailsFields: [],
    credentialFields: [
      {
        key: "use_case",
        label: "Intended use",
        required: true,
        placeholder: "e.g. Urban mobility research",
      },
      {
        key: "datasets",
        label: "Datasets of interest",
        placeholder: "e.g. GTFS-RT, telemetry aggregates",
      },
    ],
    outcome: "Data-sharing requests are reviewed by the open-data team.",
  },
  {
    id: "gov-viewer",
    label: "Gov viewer",
    tagline: "City KPI dashboard: cost, emissions, ridership and uptime.",
    icon: Landmark,
    instant: false,
    detailsFields: [],
    credentialFields: [
      {
        key: "department",
        label: "Department",
        required: true,
        placeholder: "e.g. Transport Authority",
      },
      {
        key: "gov_id",
        label: "Employee / badge id",
        placeholder: "Optional",
      },
    ],
    outcome: "Municipal access is approved by a platform administrator.",
  },
];

export function personaById(id: OnboardingPersona): PersonaDef {
  return PERSONAS.find((p) => p.id === id) ?? PERSONAS[0]!;
}
