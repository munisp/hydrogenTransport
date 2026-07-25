import { useMemo, useState } from "react";
import { useMutation } from "@tanstack/react-query";
import {
  ArrowLeft,
  ArrowRight,
  CheckCircle2,
  Clock,
  Droplet,
  LogIn,
  PartyPopper,
} from "lucide-react";
import { onboardCitizen, submitOnboarding, type OnboardingPersona } from "../../api/admin";
import { ApiError } from "../../api/client";
import { useAuth } from "../../auth/AuthContext";
import { Button, Card, Field, Input } from "../../components/ui";
import { cn } from "../../lib/utils";
import { PERSONAS, personaById, type PersonaDef } from "./personas";

/**
 * Public pre-auth landing page (/welcome): persona cards + a tailored
 * multi-step onboarding wizard per stakeholder type. Citizen sign-up is
 * instant (POST /v1/onboarding/citizen); every other persona submits an
 * approval-gated intake request (POST /v1/onboarding/{persona}).
 */

const STEPS = ["Your details", "Organisation", "Review", "Done"] as const;

interface FormState {
  displayName: string;
  email: string;
  password: string;
  org: string;
  /** Persona-specific answers collected into the intake `meta` object. */
  extra: Record<string, string>;
}

const emptyForm: FormState = { displayName: "", email: "", password: "", org: "", extra: {} };

const EMAIL_RE = /^[^@\s]+@[^@\s]+\.[^@\s]+$/;

export default function WelcomePage() {
  const { identity, login } = useAuth();
  const [personaId, setPersonaId] = useState<OnboardingPersona | null>(null);
  const persona = personaId ? personaById(personaId) : null;

  return (
    <div className="min-h-screen bg-surface">
      {/* Public header */}
      <header className="border-b border-stone-200 bg-surface-raised">
        <div className="mx-auto flex max-w-5xl items-center gap-3 px-4 py-4 md:px-8">
          <span className="flex h-9 w-9 items-center justify-center rounded-lg bg-accent text-white">
            <Droplet className="h-5 w-5" aria-hidden />
          </span>
          <div className="flex-1">
            <p className="text-sm font-semibold tracking-tight text-stone-900">H2Fleet</p>
            <p className="text-xs text-stone-500">Hydrogen bus platform</p>
          </div>
          {identity.authenticated ? (
            <Button variant="secondary" onClick={() => (window.location.href = "/")}>
              Open console
            </Button>
          ) : (
            <Button variant="secondary" onClick={login}>
              <LogIn className="h-4 w-4" aria-hidden /> Sign in
            </Button>
          )}
        </div>
      </header>

      <main className="mx-auto max-w-5xl px-4 py-10 md:px-8">
        {persona ? (
          <OnboardingWizard persona={persona} onBack={() => setPersonaId(null)} />
        ) : (
          <PersonaSelect onSelect={setPersonaId} />
        )}
      </main>

      <footer className="border-t border-stone-200 py-6 text-center text-xs text-stone-400">
        H2Fleet — clean hydrogen public transport. Accounts are verified before
        operational access is granted.
      </footer>
    </div>
  );
}

// ---- Step 0: persona cards -----------------------------------------------------

function PersonaSelect({ onSelect }: { onSelect: (p: OnboardingPersona) => void }) {
  return (
    <div>
      <div className="max-w-2xl">
        <h1 className="text-3xl font-semibold tracking-tight text-stone-900">
          Join the hydrogen network
        </h1>
        <p className="mt-2 text-sm leading-6 text-stone-500">
          One platform for riders, drivers, operators, station crews, advertisers,
          data partners and city government. Pick the role that fits you — citizens
          get an account instantly, operational roles are approved by the team.
        </p>
      </div>

      <div
        className="mt-8 grid grid-cols-1 gap-4 sm:grid-cols-2 lg:grid-cols-3"
        role="list"
        aria-label="Choose your role"
      >
        {PERSONAS.map((p) => (
          <button
            key={p.id}
            type="button"
            role="listitem"
            onClick={() => onSelect(p.id)}
            className="group flex flex-col rounded-xl border border-stone-200 bg-surface-raised p-5 text-left shadow-card transition-all hover:-translate-y-0.5 hover:border-accent hover:shadow-md focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-accent"
          >
            <div className="flex items-center justify-between">
              <span className="rounded-lg bg-accent-soft p-2 text-accent transition-colors group-hover:bg-accent group-hover:text-white">
                <p.icon className="h-5 w-5" aria-hidden />
              </span>
              {p.instant ? (
                <span className="rounded-full bg-teal-50 px-2 py-0.5 text-[11px] font-medium text-teal-800 ring-1 ring-inset ring-teal-200">
                  instant access
                </span>
              ) : (
                <span className="rounded-full bg-stone-100 px-2 py-0.5 text-[11px] font-medium text-stone-600 ring-1 ring-inset ring-stone-200">
                  approval required
                </span>
              )}
            </div>
            <p className="mt-4 text-sm font-semibold text-stone-900">{p.label}</p>
            <p className="mt-1 flex-1 text-xs leading-5 text-stone-500">{p.tagline}</p>
            <span className="mt-4 inline-flex items-center gap-1 text-xs font-medium text-accent">
              Get started <ArrowRight className="h-3.5 w-3.5" aria-hidden />
            </span>
          </button>
        ))}
      </div>
    </div>
  );
}

// ---- Steps 1..4: wizard ----------------------------------------------------------

function OnboardingWizard({ persona, onBack }: { persona: PersonaDef; onBack: () => void }) {
  const { login } = useAuth();
  const [step, setStep] = useState(0);
  const [form, setForm] = useState<FormState>(emptyForm);
  const [touchedSubmit, setTouchedSubmit] = useState(false);

  const set = (patch: Partial<FormState>) => setForm((f) => ({ ...f, ...patch }));
  const setExtra = (key: string, value: string) =>
    setForm((f) => ({ ...f, extra: { ...f.extra, [key]: value } }));

  const detailsValid = useMemo(() => {
    if (form.displayName.trim().length === 0 || form.displayName.trim().length > 120) return false;
    if (!EMAIL_RE.test(form.email.trim())) return false;
    if (persona.instant && form.password.length < 8) return false;
    return persona.detailsFields.every(
      (f) => !f.required || (form.extra[f.key] ?? "").trim().length > 0,
    );
  }, [form, persona]);

  const credentialsValid = useMemo(() => {
    if (!persona.instant && form.org.trim().length === 0) return false;
    return persona.credentialFields.every(
      (f) => !f.required || (form.extra[f.key] ?? "").trim().length > 0,
    );
  }, [form, persona]);

  const submit = useMutation({
    mutationFn: async () => {
      if (persona.instant) {
        return onboardCitizen({
          email: form.email.trim(),
          display_name: form.displayName.trim(),
          password: form.password,
        });
      }
      const meta = Object.fromEntries(
        Object.entries(form.extra).filter(([, v]) => v.trim().length > 0),
      );
      return submitOnboarding(persona.id as Exclude<OnboardingPersona, "citizen">, {
        email: form.email.trim(),
        display_name: form.displayName.trim(),
        org: form.org.trim(),
        meta,
      });
    },
    onSuccess: () => setStep(3),
  });

  const stepValid = step === 0 ? detailsValid : step === 1 ? credentialsValid : true;
  const errorMessage =
    submit.error instanceof ApiError
      ? submit.error.body
        ? `${submit.error.message}`
        : submit.error.message
      : submit.error instanceof Error
        ? submit.error.message
        : null;

  return (
    <div className="mx-auto max-w-2xl">
      <button
        type="button"
        onClick={onBack}
        className="inline-flex items-center gap-1.5 text-sm text-stone-500 hover:text-stone-800 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-accent"
      >
        <ArrowLeft className="h-4 w-4" aria-hidden /> All roles
      </button>

      <div className="mt-4 flex items-start gap-3">
        <span className="rounded-lg bg-accent-soft p-2.5 text-accent">
          <persona.icon className="h-6 w-6" aria-hidden />
        </span>
        <div>
          <h1 className="text-2xl font-semibold tracking-tight text-stone-900">
            {persona.label} onboarding
          </h1>
          <p className="mt-1 text-sm text-stone-500">{persona.outcome}</p>
        </div>
      </div>

      {/* Stepper */}
      <ol aria-label="Onboarding progress" className="mt-8 flex items-center gap-2">
        {STEPS.map((label, i) => {
          const state = i < step ? "done" : i === step ? "current" : "todo";
          return (
            <li key={label} className="flex flex-1 items-center gap-2" aria-current={state === "current" ? "step" : undefined}>
              <span
                className={cn(
                  "flex h-7 w-7 shrink-0 items-center justify-center rounded-full text-xs font-semibold",
                  state === "done" && "bg-teal-accent text-white",
                  state === "current" && "bg-accent text-white",
                  state === "todo" && "bg-stone-200 text-stone-500",
                )}
                aria-hidden
              >
                {state === "done" ? <CheckCircle2 className="h-4 w-4" /> : i + 1}
              </span>
              <span
                className={cn(
                  "hidden text-xs sm:block",
                  state === "current" ? "font-medium text-stone-800" : "text-stone-400",
                )}
              >
                {label}
              </span>
              {i < STEPS.length - 1 ? <span className="h-px flex-1 bg-stone-200" aria-hidden /> : null}
            </li>
          );
        })}
      </ol>
      <p className="sr-only" aria-live="polite">
        Step {step + 1} of {STEPS.length}: {STEPS[step]}
      </p>

      <Card className="mt-6 p-6">
        {step === 0 ? (
          <fieldset>
            <legend className="text-sm font-semibold text-stone-800">Your details</legend>
            <div className="mt-4 space-y-4">
              <Field label="Full name">
                <Input
                  value={form.displayName}
                  onChange={(e) => set({ displayName: e.target.value })}
                  placeholder="Ada Lovelace"
                  autoComplete="name"
                  required
                />
              </Field>
              <Field label="Email address">
                <Input
                  type="email"
                  value={form.email}
                  onChange={(e) => set({ email: e.target.value })}
                  placeholder="you@example.org"
                  autoComplete="email"
                  required
                />
              </Field>
              {persona.detailsFields.map((f) => (
                <Field key={f.key} label={f.label} hint={f.hint}>
                  <Input
                    type={f.type ?? "text"}
                    value={form.extra[f.key] ?? ""}
                    onChange={(e) => setExtra(f.key, e.target.value)}
                    placeholder={f.placeholder}
                    required={f.required}
                    autoComplete={f.type === "password" ? "new-password" : undefined}
                  />
                </Field>
              ))}
              {persona.instant && touchedSubmit && form.password.length < 8 ? (
                <p className="text-xs text-red-700">Password needs at least 8 characters.</p>
              ) : null}
            </div>
          </fieldset>
        ) : null}

        {step === 1 ? (
          <fieldset>
            <legend className="text-sm font-semibold text-stone-800">
              {persona.instant ? "Almost done" : "Organisation & credentials"}
            </legend>
            <div className="mt-4 space-y-4">
              {persona.instant ? (
                <p className="text-sm text-stone-500">
                  No organisation needed — citizen accounts are personal and activate
                  immediately.
                </p>
              ) : (
                <Field label="Organisation">
                  <Input
                    value={form.org}
                    onChange={(e) => set({ org: e.target.value })}
                    placeholder="e.g. City Transit Authority"
                    autoComplete="organization"
                    required
                  />
                </Field>
              )}
              {persona.credentialFields.map((f) => (
                <Field key={f.key} label={f.label} hint={f.hint}>
                  <Input
                    value={form.extra[f.key] ?? ""}
                    onChange={(e) => setExtra(f.key, e.target.value)}
                    placeholder={f.placeholder}
                    required={f.required}
                  />
                </Field>
              ))}
            </div>
          </fieldset>
        ) : null}

        {step === 2 ? (
          <div>
            <h2 className="text-sm font-semibold text-stone-800">Review your request</h2>
            <dl className="mt-4 divide-y divide-stone-100 text-sm">
              <ReviewRow label="Role" value={persona.label} />
              <ReviewRow label="Name" value={form.displayName.trim()} />
              <ReviewRow label="Email" value={form.email.trim()} />
              {!persona.instant ? <ReviewRow label="Organisation" value={form.org.trim()} /> : null}
              {persona.credentialFields.map((f) =>
                (form.extra[f.key] ?? "").trim() ? (
                  <ReviewRow key={f.key} label={f.label} value={(form.extra[f.key] ?? "").trim()} />
                ) : null,
              )}
            </dl>
            {errorMessage ? (
              <p role="alert" className="mt-4 rounded-lg border border-red-200 bg-red-50 px-3 py-2 text-xs text-red-800">
                {errorMessage}
              </p>
            ) : null}
            {persona.instant ? (
              <p className="mt-4 rounded-lg border border-teal-200 bg-teal-50 px-3 py-2 text-xs text-teal-800">
                Your account is created immediately and you can sign in straight away.
              </p>
            ) : (
              <p className="mt-4 rounded-lg border border-amber-200 bg-amber-50 px-3 py-2 text-xs text-amber-800">
                This sends an approval request to the operations team. You will receive
                an email once your account is activated.
              </p>
            )}
          </div>
        ) : null}

        {step === 3 ? (
          <div className="flex flex-col items-center py-4 text-center">
            {persona.instant ? (
              <>
                <PartyPopper className="h-10 w-10 text-teal-accent" aria-hidden />
                <h2 className="mt-4 text-lg font-semibold text-stone-900">Welcome aboard!</h2>
                <p className="mt-2 max-w-sm text-sm text-stone-500">
                  Your citizen account is ready. Check your inbox to verify your email,
                  then sign in to see arrivals, request DRT shuttles and track your
                  carbon credits.
                </p>
                <Button className="mt-6" onClick={login}>
                  <LogIn className="h-4 w-4" aria-hidden /> Sign in now
                </Button>
              </>
            ) : (
              <>
                <Clock className="h-10 w-10 text-accent" aria-hidden />
                <h2 className="mt-4 text-lg font-semibold text-stone-900">Approval pending</h2>
                <p className="mt-2 max-w-sm text-sm text-stone-500">
                  Your {persona.label.toLowerCase()} request has been submitted and is
                  waiting for review. You will receive an email at{" "}
                  <span className="font-medium text-stone-700">{form.email.trim()}</span>{" "}
                  once the team activates your account.
                </p>
                <Button variant="secondary" className="mt-6" onClick={onBack}>
                  Back to roles
                </Button>
              </>
            )}
          </div>
        ) : null}

        {/* Wizard footer */}
        {step < 3 ? (
          <div className="mt-6 flex items-center justify-between border-t border-stone-100 pt-4">
            <Button
              variant="ghost"
              onClick={() => (step === 0 ? onBack() : setStep(step - 1))}
            >
              Back
            </Button>
            {step < 2 ? (
              <Button
                onClick={() => {
                  setTouchedSubmit(true);
                  if (stepValid) setStep(step + 1);
                }}
                aria-disabled={!stepValid}
              >
                Continue <ArrowRight className="h-4 w-4" aria-hidden />
              </Button>
            ) : (
              <Button busy={submit.isPending} onClick={() => submit.mutate()}>
                {persona.instant ? "Create my account" : "Submit for approval"}
              </Button>
            )}
          </div>
        ) : null}
      </Card>
    </div>
  );
}

function ReviewRow({ label, value }: { label: string; value: string }) {
  return (
    <div className="flex items-baseline justify-between gap-6 py-2.5">
      <dt className="text-stone-500">{label}</dt>
      <dd className="text-right font-medium text-stone-800">{value}</dd>
    </div>
  );
}
