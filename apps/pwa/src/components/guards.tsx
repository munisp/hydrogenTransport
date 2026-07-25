import { Navigate, useLocation } from "react-router-dom";
import type { ReactNode } from "react";
import { ShieldX } from "lucide-react";
import { useAuth } from "../auth/AuthContext";
import { useToggles } from "../toggles/TogglesContext";
import { moduleById, MODULES, type ModuleId } from "../modules/registry";
import { EmptyState, Spinner } from "./ui";

/** Route guard: hides a page entirely when its module toggle is off (SPEC §3.2). */
export function RequireModule({ id, children }: { id: ModuleId; children: ReactNode }) {
  const { isEnabled, loading } = useToggles();
  if (loading) return <Spinner />;
  if (!isEnabled(id)) {
    const def = moduleById(id);
    return (
      <div className="mx-auto max-w-lg pt-16">
        <EmptyState
          title={`${def?.label ?? id} is disabled`}
          body="This module has been turned off in the feature toggle service. A platform administrator can re-enable it from Platform → Module Toggles; the page reappears automatically within 30 seconds."
        />
      </div>
    );
  }
  return <>{children}</>;
}

/** Route guard: Keycloak realm/client role required. */
export function RequireRole({ role, children }: { role: string; children: ReactNode }) {
  const { hasRole } = useAuth();
  const location = useLocation();
  if (!hasRole(role)) {
    return (
      <div className="mx-auto max-w-lg pt-16">
        <EmptyState
          title="Insufficient permissions"
          body={`This area requires the ${role} role. Your current Keycloak roles do not grant access.`}
        />
      </div>
    );
  }
  void location;
  return <>{children}</>;
}

/** Route guard: any one of the listed roles suffices. */
export function RequireAnyRole({ roles, children }: { roles: string[]; children: ReactNode }) {
  const { hasRole } = useAuth();
  if (!roles.some((r) => hasRole(r))) {
    return (
      <div className="mx-auto max-w-lg pt-16">
        <EmptyState
          title="Insufficient permissions"
          body={`This area requires one of: ${roles.join(", ")}. Your current Keycloak roles do not grant access.`}
        />
      </div>
    );
  }
  return <>{children}</>;
}

/**
 * Route guard for the authenticated app shell. Unauthenticated visitors are
 * sent to the public onboarding landing page (/welcome); from there the
 * "Sign in" CTA runs the Keycloak login redirect and returns here.
 */
export function RequireAuth({ children }: { children: ReactNode }) {
  const { identity } = useAuth();
  const location = useLocation();
  if (!identity.authenticated) {
    return <Navigate to="/welcome" state={{ from: location.pathname }} replace />;
  }
  return <>{children}</>;
}

/** Landing route: redirect to the first enabled module, gov-dashboard preferred. */
export function HomeRedirect() {
  const { identity } = useAuth();
  const { isEnabled, loading } = useToggles();
  if (!identity.authenticated) return <Navigate to="/welcome" replace />;
  if (loading) return <Spinner />;
  const preferred = MODULES.find((m) => m.id === "gov-dashboard" && isEnabled(m.id));
  const first = preferred ?? MODULES.find((m) => isEnabled(m.id));
  if (!first) {
    return (
      <div className="mx-auto max-w-lg pt-16">
        <div className="flex flex-col items-center gap-3 text-center">
          <ShieldX className="h-8 w-8 text-stone-300" aria-hidden />
          <EmptyState
            title="All modules are disabled"
            body="Every capability module is currently off. A platform administrator can enable modules from the toggle admin once the toggle service is reachable."
          />
        </div>
      </div>
    );
  }
  return <Navigate to={first.path} replace />;
}
