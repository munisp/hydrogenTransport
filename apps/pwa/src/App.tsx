import { Suspense, lazy } from "react";
import { BrowserRouter, Navigate, Route, Routes } from "react-router-dom";
import { Layout } from "./components/Layout";
import { HomeRedirect, RequireModule, RequireRole } from "./components/guards";
import { Spinner } from "./components/ui";
import { MODULES } from "./modules/registry";
import { useAuth } from "./auth/AuthContext";
import NotFoundPage from "./pages/NotFoundPage";

const AdminPage = lazy(() => import("./pages/AdminPage"));

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
      <Suspense
        fallback={
          <div className="flex min-h-screen items-center justify-center bg-surface">
            <Spinner />
          </div>
        }
      >
        <Routes>
          <Route element={<Layout />}>
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
              path="/admin"
              element={
                <RequireRole role="platform-admin">
                  <AdminPage />
                </RequireRole>
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
