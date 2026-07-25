import React from "react";
import ReactDOM from "react-dom/client";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import App from "./App";
import { AuthProvider } from "./auth/AuthContext";
import { TogglesProvider } from "./toggles/TogglesContext";
import { registerServiceWorker } from "./pwa-utils";
import "./index.css";

// SW registration with new-version purge + one-time reload (see pwa-utils.ts).
void registerServiceWorker();

const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      retry: 1,
      refetchOnWindowFocus: false,
      staleTime: 10_000,
    },
  },
});

ReactDOM.createRoot(document.getElementById("root") as HTMLElement).render(
  <React.StrictMode>
    <QueryClientProvider client={queryClient}>
      <AuthProvider>
        <TogglesProvider>
          <App />
        </TogglesProvider>
      </AuthProvider>
    </QueryClientProvider>
  </React.StrictMode>,
);
