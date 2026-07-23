import { useState } from "react";
import { NavLink, Outlet } from "react-router-dom";
import { Droplet, LogOut, Menu, Settings2, X } from "lucide-react";
import { useAuth } from "../auth/AuthContext";
import { useToggles } from "../toggles/TogglesContext";
import { DOMAINS, modulesByDomain } from "../modules/registry";
import { cn } from "../lib/utils";

/**
 * App shell: sidebar nav grouped by the 4 domains. Entries appear/disappear
 * live as feature toggles change (SPEC §3.7).
 */
export function Layout() {
  const { identity, hasRole, logout } = useAuth();
  const { isEnabled, error: toggleError } = useToggles();
  const [mobileOpen, setMobileOpen] = useState(false);

  const nav = (
    <nav className="flex-1 space-y-6 overflow-y-auto px-4 py-6">
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
                    onClick={() => setMobileOpen(false)}
                    className={({ isActive }) =>
                      cn(
                        "flex items-center gap-2.5 rounded-lg px-2.5 py-2 text-sm transition-colors",
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
      {hasRole("platform-admin") ? (
        <div>
          <p className="px-2 text-[11px] font-semibold uppercase tracking-wider text-stone-400">
            Platform
          </p>
          <ul className="mt-2 space-y-0.5">
            <li>
              <NavLink
                to="/admin"
                onClick={() => setMobileOpen(false)}
                className={({ isActive }) =>
                  cn(
                    "flex items-center gap-2.5 rounded-lg px-2.5 py-2 text-sm transition-colors",
                    isActive
                      ? "bg-accent-soft font-medium text-accent-muted"
                      : "text-stone-600 hover:bg-surface-sunken hover:text-stone-900",
                  )
                }
              >
                <Settings2 className="h-4 w-4 shrink-0" aria-hidden />
                Module Toggles
              </NavLink>
            </li>
          </ul>
        </div>
      ) : null}
    </nav>
  );

  return (
    <div className="flex min-h-screen bg-surface text-stone-800">
      {/* Sidebar — desktop */}
      <aside className="fixed inset-y-0 left-0 z-30 hidden w-64 flex-col border-r border-stone-200 bg-surface-raised md:flex">
        <Brand />
        {nav}
        <UserFooter
          username={identity.username}
          isDevFallback={identity.isDevFallback}
          onLogout={logout}
        />
      </aside>

      {/* Sidebar — mobile drawer */}
      {mobileOpen ? (
        <div className="fixed inset-0 z-40 md:hidden">
          <div
            className="absolute inset-0 bg-stone-900/40"
            onClick={() => setMobileOpen(false)}
            aria-hidden
          />
          <aside className="absolute inset-y-0 left-0 flex w-72 flex-col bg-surface-raised shadow-xl">
            <div className="flex items-center justify-between pr-3">
              <Brand />
              <button
                className="rounded-md p-2 text-stone-500 hover:bg-surface-sunken"
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
      <div className="flex min-h-screen flex-1 flex-col md:pl-64">
        <header className="sticky top-0 z-20 flex items-center gap-3 border-b border-stone-200 bg-surface/90 px-4 py-3 backdrop-blur md:px-8">
          <button
            className="rounded-md p-2 text-stone-500 hover:bg-surface-sunken md:hidden"
            onClick={() => setMobileOpen(true)}
            aria-label="Open menu"
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
        <main className="flex-1 px-4 py-6 md:px-8 md:py-8">
          <Outlet />
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
        className="rounded-md p-1.5 text-stone-400 hover:bg-surface-sunken hover:text-stone-600"
        onClick={onLogout}
        aria-label="Sign out"
        title="Sign out"
      >
        <LogOut className="h-4 w-4" />
      </button>
    </div>
  );
}
