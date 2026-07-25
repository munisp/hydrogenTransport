import { useEffect, useState } from "react";
import { NavLink, Outlet, useLocation } from "react-router-dom";
import { Activity, Droplet, LogOut, Menu, Settings2, X } from "lucide-react";
import { useAuth } from "../auth/AuthContext";
import { useToggles } from "../toggles/TogglesContext";
import { DOMAINS, modulesByDomain } from "../modules/registry";
import { ErrorBoundary } from "./ErrorBoundary";
import { cn } from "../lib/utils";

/**
 * App shell: sidebar nav grouped by the 4 domains. Entries appear/disappear
 * live as feature toggles change (SPEC §3.2/§3.7). Under 1024px the sidebar
 * becomes a slide-over drawer; keyboard users get a skip link and Escape
 * closes the drawer.
 */
export function Layout() {
  const { identity, hasRole, logout } = useAuth();
  const { isEnabled, error: toggleError } = useToggles();
  const [mobileOpen, setMobileOpen] = useState(false);
  const location = useLocation();
  const canOperate = hasRole("platform-admin") || hasRole("operator");

  // Close the drawer on route change and on Escape.
  useEffect(() => setMobileOpen(false), [location.pathname]);
  useEffect(() => {
    if (!mobileOpen) return;
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") setMobileOpen(false);
    };
    document.addEventListener("keydown", onKey);
    return () => document.removeEventListener("keydown", onKey);
  }, [mobileOpen]);

  const nav = (
    <nav aria-label="Primary" className="flex-1 space-y-6 overflow-y-auto px-4 py-6">
      {DOMAINS.map((domain) => {
        const enabledModules = modulesByDomain(domain.id).filter((m) => isEnabled(m.id));
        if (enabledModules.length === 0) return null;
        return (
          <div key={domain.id}>
            <p className="px-2 text-[11px] font-semibold uppercase tracking-wider text-stone-400">
              {domain.label}
            </p>
            <ul className="mt-2 space-y-0.5">
              {enabledModules.map((m) => (
                <li key={m.id}>
                  <NavLink
                    to={m.path}
                    className={({ isActive }) =>
                      cn(
                        "flex items-center gap-2.5 rounded-lg px-2.5 py-2 text-sm transition-colors focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-accent",
                        isActive
                          ? "bg-accent-soft font-medium text-accent-muted"
                          : "text-stone-600 hover:bg-surface-sunken hover:text-stone-900",
                      )
                    }
                  >
                    <m.icon className="h-4 w-4 shrink-0" aria-hidden />
                    {m.label}
                  </NavLink>
                </li>
              ))}
            </ul>
          </div>
        );
      })}
      {canOperate ? (
        <div>
          <p className="px-2 text-[11px] font-semibold uppercase tracking-wider text-stone-400">
            Platform
          </p>
          <ul className="mt-2 space-y-0.5">
            <li>
              <NavLink
                to="/admin"
                className={({ isActive }) =>
                  cn(
                    "flex items-center gap-2.5 rounded-lg px-2.5 py-2 text-sm transition-colors focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-accent",
                    isActive
                      ? "bg-accent-soft font-medium text-accent-muted"
                      : "text-stone-600 hover:bg-surface-sunken hover:text-stone-900",
                  )
                }
              >
                <Settings2 className="h-4 w-4 shrink-0" aria-hidden />
                Admin Console
              </NavLink>
            </li>
            <li>
              <NavLink
                to="/admin/noc"
                className={({ isActive }) =>
                  cn(
                    "flex items-center gap-2.5 rounded-lg px-2.5 py-2 text-sm transition-colors focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-accent",
                    isActive
                      ? "bg-accent-soft font-medium text-accent-muted"
                      : "text-stone-600 hover:bg-surface-sunken hover:text-stone-900",
                  )
                }
              >
                <Activity className="h-4 w-4 shrink-0" aria-hidden />
                NOC Wallboard
              </NavLink>
            </li>
          </ul>
        </div>
      ) : null}
    </nav>
  );

  return (
    <div className="flex min-h-screen bg-surface text-stone-800">
      <a href="#main-content" className="skip-link">
        Skip to content
      </a>

      {/* Sidebar — desktop (≥1024px) */}
      <aside className="fixed inset-y-0 left-0 z-30 hidden w-64 flex-col border-r border-stone-200 bg-surface-raised lg:flex">
        <Brand />
        {nav}
        <UserFooter
          username={identity.username}
          isDevFallback={identity.isDevFallback}
          onLogout={logout}
        />
      </aside>

      {/* Sidebar — drawer (<1024px) */}
      {mobileOpen ? (
        <div className="fixed inset-0 z-40 lg:hidden" role="dialog" aria-modal="true" aria-label="Navigation menu">
          <div
            className="absolute inset-0 bg-stone-900/40"
            onClick={() => setMobileOpen(false)}
            aria-hidden
          />
          <aside className="absolute inset-y-0 left-0 flex w-72 flex-col bg-surface-raised shadow-xl">
            <div className="flex items-center justify-between pr-3">
              <Brand />
              <button
                className="rounded-md p-2 text-stone-500 hover:bg-surface-sunken focus-visible:outline focus-visible:outline-2 focus-visible:outline-accent"
                onClick={() => setMobileOpen(false)}
                aria-label="Close menu"
              >
                <X className="h-5 w-5" />
              </button>
            </div>
            {nav}
          </aside>
        </div>
      ) : null}

      {/* Main column */}
      <div className="flex min-h-screen flex-1 flex-col lg:pl-64">
        <header className="sticky top-0 z-20 flex items-center gap-3 border-b border-stone-200 bg-surface/90 px-4 py-3 backdrop-blur lg:px-8">
          <button
            className="rounded-md p-2 text-stone-500 hover:bg-surface-sunken focus-visible:outline focus-visible:outline-2 focus-visible:outline-accent lg:hidden"
            onClick={() => setMobileOpen(true)}
            aria-label="Open menu"
            aria-expanded={mobileOpen}
          >
            <Menu className="h-5 w-5" />
          </button>
          <div className="flex-1" />
          {toggleError ? (
            <span className="rounded-full bg-amber-50 px-3 py-1 text-xs font-medium text-amber-800 ring-1 ring-inset ring-amber-200">
              Toggle service unreachable — modules fail closed
            </span>
          ) : null}
          {identity.isDevFallback ? (
            <span className="rounded-full bg-teal-50 px-3 py-1 text-xs font-medium text-teal-800 ring-1 ring-inset ring-teal-200">
              Dev auth (mock admin)
            </span>
          ) : null}
        </header>
        <main id="main-content" className="flex-1 px-4 py-6 lg:px-8 lg:py-8" tabIndex={-1}>
          <ErrorBoundary resetKey={location.pathname}>
            <div key={location.pathname} className="page-enter">
              <Outlet />
            </div>
          </ErrorBoundary>
        </main>
      </div>
    </div>
  );
}

function Brand() {
  return (
    <div className="flex items-center gap-2.5 border-b border-stone-200 px-5 py-4">
      <span className="flex h-8 w-8 items-center justify-center rounded-lg bg-accent text-white">
        <Droplet className="h-5 w-5" aria-hidden />
      </span>
      <div>
        <p className="text-sm font-semibold tracking-tight text-stone-900">H2Fleet</p>
        <p className="text-[11px] text-stone-500">Hydrogen bus platform</p>
      </div>
    </div>
  );
}

function UserFooter({
  username,
  isDevFallback,
  onLogout,
}: {
  username: string;
  isDevFallback: boolean;
  onLogout: () => void;
}) {
  return (
    <div className="flex items-center gap-3 border-t border-stone-200 px-5 py-4">
      <span className="flex h-8 w-8 items-center justify-center rounded-full bg-stone-200 text-xs font-semibold text-stone-600">
        {username.slice(0, 2).toUpperCase()}
      </span>
      <div className="min-w-0 flex-1">
        <p className="truncate text-sm font-medium text-stone-800">{username}</p>
        <p className="text-[11px] text-stone-500">{isDevFallback ? "dev fallback" : "Keycloak SSO"}</p>
      </div>
      <button
        className="rounded-md p-1.5 text-stone-400 hover:bg-surface-sunken hover:text-stone-600 focus-visible:outline focus-visible:outline-2 focus-visible:outline-accent"
        onClick={onLogout}
        aria-label="Sign out"
        title="Sign out"
      >
        <LogOut className="h-4 w-4" />
      </button>
    </div>
  );
}
