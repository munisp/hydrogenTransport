import { useState } from "react";
import { useMutation } from "@tanstack/react-query";
import { setToggle } from "../api/toggles";
import { useToggles } from "../toggles/TogglesContext";
import { DOMAINS, modulesByDomain } from "../modules/registry";
import {
  Card,
  CardContent,
  CardHeader,
  CardTitle,
  PageHeader,
  Spinner,
  Switch,
} from "../components/ui";

/**
 * Platform admin: toggle switches for all 20 modules (PUT /api/toggles/{module}),
 * grouped by domain. Requires the Keycloak `platform-admin` role (enforced by
 * RequireRole at the route level and by the toggle-service itself).
 */
export default function AdminPage() {
  const { toggles, isEnabled, loading, refresh } = useToggles();
  const [lastError, setLastError] = useState<string | null>(null);

  const mutation = useMutation({
    mutationFn: ({ module, enabled }: { module: string; enabled: boolean }) =>
      setToggle(module, enabled),
    onSuccess: () => {
      setLastError(null);
      refresh();
    },
    onError: (err) => {
      setLastError(err instanceof Error ? err.message : "Toggle update failed");
    },
  });

  if (loading) return <Spinner />;

  return (
    <div>
      <PageHeader
        title="Module Toggles"
        description="Enable or disable capability modules per deployment. Disabled modules return 404 from their APIs, disappear from navigation, and idle their consumers."
      />

      {lastError ? (
        <div className="mb-4 rounded-xl border border-red-200 bg-red-50 px-4 py-3 text-sm text-red-800">
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
                const enabled = isEnabled(m.id);
                const pending =
                  mutation.isPending && mutation.variables?.module === m.id;
                return (
                  <div key={m.id} className="flex items-center gap-4 px-5 py-3.5">
                    <m.icon className="h-4 w-4 shrink-0 text-stone-400" aria-hidden />
                    <div className="min-w-0 flex-1">
                      <p className="text-sm font-medium text-stone-800">{m.label}</p>
                      <p className="truncate text-xs text-stone-500">{m.description}</p>
                      <p className="mt-0.5 font-mono text-[10px] text-stone-400">{m.id}</p>
                    </div>
                    <Switch
                      label={`Toggle ${m.label}`}
                      checked={enabled}
                      disabled={pending || !(m.id in toggles)}
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
        this UI polls the toggle service every 30 seconds.
      </p>
    </div>
  );
}
