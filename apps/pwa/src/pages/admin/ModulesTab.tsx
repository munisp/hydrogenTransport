import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { listAdminToggles, setAdminToggle, type AdminToggle } from "../../api/admin";
import { useAuth } from "../../auth/AuthContext";
import { toggleClient } from "../../api/toggles";
import { DOMAINS, modulesByDomain } from "../../modules/registry";
import {
  Badge,
  Card,
  CardContent,
  CardHeader,
  CardTitle,
  ErrorState,
  Skeleton,
  Switch,
} from "../../components/ui";

/**
 * Modules tab: the 20-toggle board enriched with owning-service labels from
 * GET /v1/admin/toggles (admin-api joins toggle-service state with the static
 * module registry). Writes require platform-admin; operators see read-only
 * switches.
 */
export default function ModulesTab() {
  const { hasRole } = useAuth();
  const canWrite = hasRole("platform-admin");
  const queryClient = useQueryClient();
  const [lastError, setLastError] = useState<string | null>(null);

  const toggles = useQuery({
    queryKey: ["admin", "toggles"],
    queryFn: listAdminToggles,
    refetchInterval: 30_000,
  });

  const byModule = new Map<string, AdminToggle>(
    (toggles.data ?? []).map((t) => [t.module, t]),
  );

  const mutation = useMutation({
    mutationFn: ({ module, enabled }: { module: string; enabled: boolean }) =>
      setAdminToggle(module, enabled),
    onSuccess: () => {
      setLastError(null);
      toggleClient.invalidate(); // refresh the app-shell toggle cache too
      queryClient.invalidateQueries({ queryKey: ["admin", "toggles"] });
    },
    onError: (err) => {
      setLastError(err instanceof Error ? err.message : "Toggle update failed");
    },
  });

  if (toggles.isLoading) {
    return (
      <div className="grid grid-cols-1 gap-4 xl:grid-cols-2" aria-busy="true" aria-label="Loading modules">
        {DOMAINS.map((d) => (
          <Card key={d.id} className="p-5">
            <Skeleton className="h-4 w-40" />
            <div className="mt-4 space-y-3">
              {[0, 1, 2].map((i) => (
                <Skeleton key={i} className="h-10 w-full" />
              ))}
            </div>
          </Card>
        ))}
      </div>
    );
  }
  if (toggles.isError) return <ErrorState error={toggles.error} onRetry={() => toggles.refetch()} />;

  return (
    <div>
      {!canWrite ? (
        <p className="mb-4 rounded-xl border border-stone-200 bg-surface-sunken px-4 py-3 text-xs text-stone-600">
          You have operator read access. Enabling or disabling modules requires the
          platform-admin role.
        </p>
      ) : null}
      {lastError ? (
        <div className="mb-4 rounded-xl border border-red-200 bg-red-50 px-4 py-3 text-sm text-red-800" role="alert">
          {lastError}
        </div>
      ) : null}

      <div className="grid grid-cols-1 gap-4 xl:grid-cols-2">
        {DOMAINS.map((domain) => (
          <Card key={domain.id}>
            <CardHeader>
              <CardTitle>{domain.label}</CardTitle>
              <p className="text-xs text-stone-500">{domain.description}</p>
            </CardHeader>
            <CardContent className="divide-y divide-stone-100 p-0">
              {modulesByDomain(domain.id).map((m) => {
                const row = byModule.get(m.id);
                const enabled = row?.enabled === true;
                const pending = mutation.isPending && mutation.variables?.module === m.id;
                return (
                  <div key={m.id} className="flex items-center gap-4 px-5 py-3.5">
                    <m.icon className="h-4 w-4 shrink-0 text-stone-400" aria-hidden />
                    <div className="min-w-0 flex-1">
                      <p className="text-sm font-medium text-stone-800">{m.label}</p>
                      <p className="truncate text-xs text-stone-500">{m.description}</p>
                      <p className="mt-1 flex flex-wrap items-center gap-1">
                        <span className="font-mono text-[10px] text-stone-400">{m.id}</span>
                        {(row?.owning_services ?? []).map((svc) => (
                          <Badge key={svc} tone="stone" className="px-1.5 py-0 text-[10px]">
                            {svc}
                          </Badge>
                        ))}
                      </p>
                    </div>
                    <Switch
                      label={`Toggle ${m.label}`}
                      checked={enabled}
                      disabled={pending || !canWrite || !row}
                      onChange={(next) => mutation.mutate({ module: m.id, enabled: next })}
                    />
                  </div>
                );
              })}
            </CardContent>
          </Card>
        ))}
      </div>

      <p className="mt-6 text-xs text-stone-400">
        Changes publish a toggle.changed event and propagate to all services within seconds;
        this board polls the admin feed every 30 seconds.
      </p>
    </div>
  );
}
