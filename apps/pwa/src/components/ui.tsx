import { forwardRef, useEffect, type ButtonHTMLAttributes, type HTMLAttributes, type InputHTMLAttributes, type ReactNode, type SelectHTMLAttributes, type TdHTMLAttributes, type ThHTMLAttributes } from "react";
import { Loader2, X } from "lucide-react";
import { cn } from "../lib/utils";

/** Hand-rolled shadcn-style primitives on the warm stone/amber/teal palette. */

// ---- Card -------------------------------------------------------------------

export function Card({ className, ...props }: HTMLAttributes<HTMLDivElement>) {
  return (
    <div
      className={cn(
        "rounded-xl border border-stone-200 bg-surface-raised shadow-card",
        className,
      )}
      {...props}
    />
  );
}

export function CardHeader({ className, ...props }: HTMLAttributes<HTMLDivElement>) {
  return <div className={cn("flex flex-col gap-1 p-5 pb-3", className)} {...props} />;
}

export function CardTitle({ className, ...props }: HTMLAttributes<HTMLHeadingElement>) {
  return (
    <h3
      className={cn("text-sm font-semibold tracking-tight text-stone-800", className)}
      {...props}
    />
  );
}

export function CardDescription({ className, ...props }: HTMLAttributes<HTMLParagraphElement>) {
  return <p className={cn("text-xs text-stone-500", className)} {...props} />;
}

export function CardContent({ className, ...props }: HTMLAttributes<HTMLDivElement>) {
  return <div className={cn("p-5 pt-2", className)} {...props} />;
}

// ---- Button -----------------------------------------------------------------

type ButtonVariant = "primary" | "secondary" | "ghost" | "danger";

const buttonVariants: Record<ButtonVariant, string> = {
  primary: "bg-accent text-white hover:bg-accent-muted disabled:bg-stone-300",
  secondary:
    "border border-stone-300 bg-surface-raised text-stone-700 hover:bg-surface-sunken disabled:text-stone-400",
  ghost: "text-stone-600 hover:bg-surface-sunken disabled:text-stone-400",
  danger: "bg-red-700 text-white hover:bg-red-800 disabled:bg-stone-300",
};

export interface ButtonProps extends ButtonHTMLAttributes<HTMLButtonElement> {
  variant?: ButtonVariant;
  busy?: boolean;
}

export const Button = forwardRef<HTMLButtonElement, ButtonProps>(function Button(
  { className, variant = "primary", busy = false, disabled, children, ...props },
  ref,
) {
  return (
    <button
      ref={ref}
      disabled={disabled || busy}
      className={cn(
        "inline-flex items-center justify-center gap-2 rounded-lg px-3.5 py-2 text-sm font-medium transition-colors focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-accent disabled:cursor-not-allowed",
        buttonVariants[variant],
        className,
      )}
      {...props}
    >
      {busy ? <Loader2 className="h-4 w-4 animate-spin" aria-hidden /> : null}
      {children}
    </button>
  );
});

// ---- Badge ------------------------------------------------------------------

type BadgeTone = "neutral" | "green" | "amber" | "red" | "teal" | "stone";

const badgeTones: Record<BadgeTone, string> = {
  neutral: "bg-stone-100 text-stone-700 ring-stone-200",
  stone: "bg-stone-100 text-stone-600 ring-stone-200",
  green: "bg-emerald-50 text-emerald-800 ring-emerald-200",
  amber: "bg-amber-50 text-amber-800 ring-amber-200",
  red: "bg-red-50 text-red-800 ring-red-200",
  teal: "bg-teal-50 text-teal-800 ring-teal-200",
};

export function Badge({
  tone = "neutral",
  className,
  ...props
}: HTMLAttributes<HTMLSpanElement> & { tone?: BadgeTone }) {
  return (
    <span
      className={cn(
        "inline-flex items-center rounded-full px-2 py-0.5 text-xs font-medium ring-1 ring-inset",
        badgeTones[tone],
        className,
      )}
      {...props}
    />
  );
}

/** Map a domain status string to a badge tone. */
export function statusTone(status: string): BadgeTone {
  switch (status) {
    case "in_service":
    case "online":
    case "active":
    case "settled":
    case "completed":
    case "done":
    case "final":
    case "submitted":
    case "free":
    case "issued":
    case "resolved":
    case "accepted":
    case "matched":
    case "approved":
    case "up":
      return "green";
    case "refueling":
    case "degraded":
    case "acknowledged":
    case "in_progress":
    case "initiated":
    case "assigned":
    case "open":
    case "requested":
    case "en_route":
    case "draft":
    case "paused":
    case "occupied":
    case "pending":
    case "insufficient-data":
      return "amber";
    case "offline":
    case "failed":
    case "critical":
    case "out_of_service":
    case "cancelled":
    case "rejected":
    case "down":
    case "drift":
      return "red";
    case "maintenance":
    case "executed":
      return "teal";
    default:
      return "stone";
  }
}

export function StatusBadge({ status, className }: { status: string; className?: string }) {
  return (
    <Badge tone={statusTone(status)} className={className}>
      {status.replace(/_/g, " ")}
    </Badge>
  );
}

// ---- Stat card (KPI) --------------------------------------------------------

export function StatCard({
  label,
  value,
  hint,
  icon,
}: {
  label: string;
  value: ReactNode;
  hint?: string;
  icon?: ReactNode;
}) {
  return (
    <Card className="p-5">
      <div className="flex items-start justify-between gap-3">
        <div>
          <p className="text-xs font-medium uppercase tracking-wide text-stone-500">{label}</p>
          <p className="mt-2 text-2xl font-semibold tabular-nums text-stone-900">{value}</p>
          {hint ? <p className="mt-1 text-xs text-stone-500">{hint}</p> : null}
        </div>
        {icon ? (
          <div className="rounded-lg bg-accent-soft p-2 text-accent" aria-hidden>
            {icon}
          </div>
        ) : null}
      </div>
    </Card>
  );
}

// ---- Switch (toggle) --------------------------------------------------------

export function Switch({
  checked,
  onChange,
  disabled,
  label,
}: {
  checked: boolean;
  onChange: (next: boolean) => void;
  disabled?: boolean;
  label: string;
}) {
  return (
    <button
      type="button"
      role="switch"
      aria-checked={checked}
      aria-label={label}
      disabled={disabled}
      onClick={() => onChange(!checked)}
      className={cn(
        "relative inline-flex h-6 w-11 shrink-0 items-center rounded-full transition-colors focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-accent disabled:cursor-not-allowed disabled:opacity-50",
        checked ? "bg-teal-accent" : "bg-stone-300",
      )}
    >
      <span
        className={cn(
          "inline-block h-5 w-5 transform rounded-full bg-white shadow transition-transform",
          checked ? "translate-x-[22px]" : "translate-x-[2px]",
        )}
      />
    </button>
  );
}

// ---- Form fields ------------------------------------------------------------

export function Field({ label, children, hint }: { label: string; children: ReactNode; hint?: string }) {
  return (
    <label className="block">
      <span className="mb-1 block text-xs font-medium text-stone-600">{label}</span>
      {children}
      {hint ? <span className="mt-1 block text-xs text-stone-400">{hint}</span> : null}
    </label>
  );
}

export function Input({ className, ...props }: InputHTMLAttributes<HTMLInputElement>) {
  return (
    <input
      className={cn(
        "w-full rounded-lg border border-stone-300 bg-surface-raised px-3 py-2 text-sm text-stone-800 placeholder:text-stone-400 focus:border-accent focus:outline-none focus:ring-1 focus:ring-accent",
        className,
      )}
      {...props}
    />
  );
}

export function Select({ className, ...props }: SelectHTMLAttributes<HTMLSelectElement>) {
  return (
    <select
      className={cn(
        "w-full rounded-lg border border-stone-300 bg-surface-raised px-3 py-2 text-sm text-stone-800 focus:border-accent focus:outline-none focus:ring-1 focus:ring-accent",
        className,
      )}
      {...props}
    />
  );
}

// ---- Progress / gauge -------------------------------------------------------

export function ProgressBar({
  valuePct,
  tone = "auto",
  className,
}: {
  valuePct: number;
  tone?: "auto" | "teal" | "amber" | "red";
  className?: string;
}) {
  const pct = Math.max(0, Math.min(100, valuePct));
  const resolved =
    tone === "auto" ? (pct < 20 ? "red" : pct < 50 ? "amber" : "teal") : tone;
  const colors: Record<string, string> = {
    teal: "bg-teal-accent",
    amber: "bg-amber-600",
    red: "bg-red-600",
  };
  return (
    <div className={cn("h-2 w-full overflow-hidden rounded-full bg-stone-200", className)}>
      <div
        className={cn("h-full rounded-full transition-all", colors[resolved])}
        style={{ width: `${pct}%` }}
        role="progressbar"
        aria-valuenow={Math.round(pct)}
        aria-valuemin={0}
        aria-valuemax={100}
      />
    </div>
  );
}

// ---- State placeholders -----------------------------------------------------

export function Spinner({ className }: { className?: string }) {
  return (
    <div className={cn("flex items-center justify-center py-12", className)}>
      <Loader2 className="h-6 w-6 animate-spin text-stone-400" aria-label="Loading" />
    </div>
  );
}

/** Pulsing placeholder block for loading skeletons. */
export function Skeleton({ className }: { className?: string }) {
  return <div className={cn("animate-pulse rounded-md bg-stone-200/70", className)} aria-hidden />;
}

/** Generic full-page loading skeleton used by route-level Suspense fallbacks. */
export function PageSkeleton() {
  return (
    <div className="mx-auto w-full max-w-5xl px-4 py-10 md:px-8" aria-busy="true" aria-label="Loading page">
      <Skeleton className="h-7 w-56" />
      <Skeleton className="mt-2 h-4 w-96 max-w-full" />
      <div className="mt-8 grid grid-cols-1 gap-4 sm:grid-cols-2 xl:grid-cols-3">
        {[0, 1, 2].map((i) => (
          <Skeleton key={i} className="h-32 w-full rounded-xl" />
        ))}
      </div>
      <Skeleton className="mt-4 h-48 w-full rounded-xl" />
    </div>
  );
}

export function EmptyState({
  title,
  body,
  action,
  icon,
}: {
  title: string;
  body?: string;
  action?: ReactNode;
  icon?: ReactNode;
}) {
  return (
    <div className="flex flex-col items-center gap-2 rounded-xl border border-dashed border-stone-300 bg-surface-sunken px-6 py-12 text-center">
      {icon ? (
        <span className="mb-1 rounded-full bg-surface-raised p-3 text-stone-300" aria-hidden>
          {icon}
        </span>
      ) : null}
      <p className="text-sm font-medium text-stone-700">{title}</p>
      {body ? <p className="max-w-md text-xs text-stone-500">{body}</p> : null}
      {action}
    </div>
  );
}

export function ErrorState({ error, onRetry }: { error: unknown; onRetry?: () => void }) {
  const message = error instanceof Error ? error.message : "Something went wrong";
  return (
    <EmptyState
      title="Couldn't load this data"
      body={`${message} — check that the domain service is reachable through APISIX (:9080).`}
      action={
        onRetry ? (
          <Button variant="secondary" className="mt-2" onClick={onRetry}>
            Retry
          </Button>
        ) : undefined
      }
    />
  );
}

// ---- Modal --------------------------------------------------------------------

export function Modal({
  title,
  onClose,
  children,
}: {
  title: string;
  onClose: () => void;
  children: ReactNode;
}) {
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") onClose();
    };
    document.addEventListener("keydown", onKey);
    return () => document.removeEventListener("keydown", onKey);
  }, [onClose]);

  return (
    <div
      className="fixed inset-0 z-50 flex items-end justify-center bg-stone-900/40 p-4 sm:items-center"
      onClick={(e) => {
        if (e.target === e.currentTarget) onClose();
      }}
    >
      <div
        role="dialog"
        aria-modal="true"
        aria-label={title}
        className="w-full max-w-md rounded-xl border border-stone-200 bg-surface-raised p-6 shadow-xl"
      >
        <div className="mb-4 flex items-center justify-between gap-4">
          <h2 className="text-base font-semibold tracking-tight text-stone-900">{title}</h2>
          <button
            type="button"
            onClick={onClose}
            aria-label="Close dialog"
            className="rounded-md p-1.5 text-stone-400 hover:bg-surface-sunken hover:text-stone-600 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-accent"
          >
            <X className="h-4 w-4" aria-hidden />
          </button>
        </div>
        {children}
      </div>
    </div>
  );
}

// ---- Page header ------------------------------------------------------------

export function PageHeader({
  title,
  description,
  actions,
}: {
  title: string;
  description?: string;
  actions?: ReactNode;
}) {
  return (
    <div className="mb-6 flex flex-wrap items-start justify-between gap-4">
      <div>
        <h1 className="text-xl font-semibold tracking-tight text-stone-900">{title}</h1>
        {description ? (
          <p className="mt-1 max-w-2xl text-sm text-stone-500">{description}</p>
        ) : null}
      </div>
      {actions ? <div className="flex items-center gap-2">{actions}</div> : null}
    </div>
  );
}

// ---- Table ------------------------------------------------------------------

export function Table({ className, ...props }: HTMLAttributes<HTMLTableElement>) {
  return (
    <div className="overflow-x-auto">
      <table className={cn("w-full text-left text-sm", className)} {...props} />
    </div>
  );
}

export function Th({ className, ...props }: ThHTMLAttributes<HTMLTableCellElement>) {
  return (
    <th
      className={cn(
        "border-b border-stone-200 px-4 py-2.5 text-xs font-medium uppercase tracking-wide text-stone-500",
        className,
      )}
      {...props}
    />
  );
}

export function Td({ className, ...props }: TdHTMLAttributes<HTMLTableCellElement>) {
  return (
    <td
      className={cn("border-b border-stone-100 px-4 py-2.5 text-stone-700", className)}
      {...props}
    />
  );
}
