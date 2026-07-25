import { Suspense, lazy } from "react";
import { BrowserRouter, Navigate, Route, Routes } from "react-router-dom";
import { Layout } from "./components/Layout";
import { HomeRedirect, RequireAnyRole, RequireAuth, RequireModule } from "./components/guards";
import { PageSkeleton, Spinner } from "./components/ui";
import { MODULES } from "./modules/registry";
import { useAuth } from "./auth/AuthContext";
import NotFoundPage from "./pages/NotFoundPage";

const WelcomePage = lazy(() => import("./pages/welcome/WelcomePage"));
const AdminPage = lazy(() => import("./pages/AdminPage"));
const NocPage = lazy(() => import("./pages/admin/NocPage"));

const OPERATOR_ROLES = ["operator", "platform-admin"];

export default function App() {
  const { ready, error } = useAuth();

  if (!ready) {
    return (
      <div className="flex min-h-screen items-center justify-center bg-surface">
        <Spinner />
      </div>
    );
  }

  if (error) {
    return (
      <div className="flex min-h-screen items-center justify-center bg-surface px-6">
        <div className="max-w-md rounded-xl border border-red-200 bg-red-50 p-6 text-center">
          <h1 className="text-sm font-semibold text-red-800">Sign-in unavailable</h1>
          <p className="mt-2 text-xs text-red-700">
            {error.message}. Keycloak must be reachable for production sign-in; check
            VITE_KEYCLOAK_URL and the realm configuration.
          </p>
        </div>
      </div>
    );
  }

  return (
    <BrowserRouter>
      <Suspense fallback={<PageSkeleton />}>
        <Routes>
          {/* Public pre-auth onboarding area (no login required). */}
          <Route path="/welcome" element={<WelcomePage />} />

          {/* Full-screen NOC wallboard (authenticated, outside the app shell). */}
          <Route
            path="/admin/noc"
            element={
              <RequireAuth>
                <RequireAnyRole roles={OPERATOR_ROLES}>
                  <NocPage />
                </RequireAnyRole>
              </RequireAuth>
            }
          />

          {/* Authenticated app shell. */}
          <Route
            element={
              <RequireAuth>
                <Layout />
              </RequireAuth>
            }
          >
            <Route index element={<HomeRedirect />} />
            {MODULES.map((m) => (
              <Route
                key={m.id}
                path={m.path}
                element={
                  <RequireModule id={m.id}>
                    <m.component />
                  </RequireModule>
                }
              />
            ))}
            <Route
              path="/admin/*"
              element={
                <RequireAnyRole roles={OPERATOR_ROLES}>
                  <AdminPage />
                </RequireAnyRole>
              }
            />
            <Route path="/home" element={<Navigate to="/" replace />} />
            <Route path="*" element={<NotFoundPage />} />
          </Route>
        </Routes>
      </Suspense>
    </BrowserRouter>
  );
}
