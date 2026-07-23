import { useQuery, useQueryClient } from "@tanstack/react-query";
import { createContext, useCallback, useContext, useMemo, type ReactNode } from "react";
import { toggleClient } from "../api/toggles";
import { config } from "../config";
import type { ModuleId } from "../modules/registry";

interface TogglesContextValue {
  /** Full toggle map; empty until the first successful fetch. */
  toggles: Record<string, boolean>;
  /** Fail-closed check: unknown or unloaded modules are treated as disabled. */
  isEnabled: (module: ModuleId | string) => boolean;
  loading: boolean;
  error: Error | null;
  /** Force an immediate re-fetch (used after admin writes). */
  refresh: () => void;
}

const TogglesContext = createContext<TogglesContextValue | null>(null);

/**
 * Boot-time toggle state (SPEC §3.2): fetch GET /api/toggles once, then poll
 * every 30s so disabled modules vanish from nav/routes without a reload.
 */
export function TogglesProvider({ children }: { children: ReactNode }) {
  const queryClient = useQueryClient();
  const query = useQuery({
    queryKey: ["toggles"],
    queryFn: () => toggleClient.getAll(),
    refetchInterval: config.togglePollMs,
    refetchOnWindowFocus: true,
    staleTime: 5_000,
    retry: 1,
  });

  const toggles = useMemo(() => query.data ?? {}, [query.data]);

  const isEnabled = useCallback(
    (module: ModuleId | string) => toggles[module] === true,
    [toggles],
  );

  const refresh = useCallback(() => {
    toggleClient.invalidate();
    void queryClient.invalidateQueries({ queryKey: ["toggles"] });
  }, [queryClient]);

  const value = useMemo<TogglesContextValue>(
    () => ({
      toggles,
      isEnabled,
      loading: query.isLoading,
      error: query.error instanceof Error ? query.error : null,
      refresh,
    }),
    [toggles, isEnabled, query.isLoading, query.error, refresh],
  );

  return <TogglesContext.Provider value={value}>{children}</TogglesContext.Provider>;
}

export function useToggles(): TogglesContextValue {
  const ctx = useContext(TogglesContext);
  if (!ctx) throw new Error("useToggles must be used inside <TogglesProvider>");
  return ctx;
}
