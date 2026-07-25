import { lazy, Suspense } from "react";
import { NavLink, Navigate, Route, Routes } from "react-router-dom";
import { Activity, BrainCircuit, LayoutDashboard, Settings2, Users } from "lucide-react";
import { useAuth } from "../auth/AuthContext";
import { PageHeader, PageSkeleton } from "../components/ui";
import { cn } from "../lib/utils";

const OverviewTab = lazy(() => import("./admin/OverviewTab"));
const ModulesTab = lazy(() => import("./admin/ModulesTab"));
const UsersTab = lazy(() => import("./admin/UsersTab"));
const AiLabTab = lazy(() => import("./admin/AiLabTab"));

/**
 * Unified admin console (operator + platform-admin). Sub-tabs:
 * Overview (KPIs), Modules (feature toggles), Users (accounts + onboarding
 * queue), AI Lab (model registry & drift). The NOC/SOC wallboard lives on the
 * chromeless full-screen route /admin/noc (registered in App.tsx).
 */
export default function AdminPage() {
  const { hasRole } = useAuth();
  const isAdmin = hasRole("platform-admin");

  const tabs = [
    { to: "/admin", end: true, label: "Overview", icon: LayoutDashboard },
    { to: "/admin/modules", end: false, label: "Modules", icon: Settings2 },
    { to: "/admin/users", end: false, label: isAdmin ? "Users & Onboarding" : "Onboarding", icon: Users },
    { to: "/admin/ai", end: false, label: "AI Lab", icon: BrainCircuit },
  ];

  return (
    <div>
      <PageHeader
        title="Admin Console"
        description="Platform operations: KPIs, module toggles, account & onboarding management, and the ML model registry."
        actions={
          <NavLink
            to="/admin/noc"
            className="inline-flex items-center gap-2 rounded-lg bg-stone-900 px-3.5 py-2 text-sm font-medium text-stone-100 transition-colors hover:bg-stone-800 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-accent"
          >
            <Activity className="h-4 w-4" aria-hidden /> NOC wallboard
          </NavLink>
        }
      />

      <div role="tablist" aria-label="Admin sections" className="mb-6 flex gap-1 overflow-x-auto border-b border-stone-200">
        {tabs.map((t) => (
          <NavLink
            key={t.to}
            to={t.to}
            end={t.end}
            role="tab"
            className={({ isActive }) =>
              cn(
                "flex items-center gap-2 whitespace-nowrap border-b-2 px-4 py-2.5 text-sm transition-colors focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-accent",
                isActive
                  ? "border-accent font-medium text-accent-muted"
                  : "border-transparent text-stone-500 hover:border-stone-300 hover:text-stone-800",
              )
            }
          >
            <t.icon className="h-4 w-4" aria-hidden />
            {t.label}
          </NavLink>
        ))}
      </div>

      <Suspense fallback={<PageSkeleton />}>
        <Routes>
          <Route index element={<OverviewTab />} />
          <Route path="modules" element={<ModulesTab />} />
          <Route path="users" element={<UsersTab />} />
          <Route path="ai" element={<AiLabTab />} />
          <Route path="*" element={<Navigate to="/admin" replace />} />
        </Routes>
      </Suspense>
    </div>
  );
}
