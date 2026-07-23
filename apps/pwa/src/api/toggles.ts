import { ToggleClient } from "@h2fleet/toggle-client";
import { getAccessToken } from "../auth/keycloak";
import { API_PREFIX, config } from "../config";

/**
 * Toggle-service access.
 *
 * Reads go through the shared SDK (SPEC §3.2: 5s cache, fail-closed). Writes
 * (PUT /v1/toggles/{module}, Keycloak role `platform-admin`) are one-shot admin
 * mutations and use a direct call — after a successful write the SDK cache is
 * invalidated so the UI reflects the change on the next poll.
 */
export const toggleClient = new ToggleClient(`${config.apiBase}${API_PREFIX.toggles}`, {
  getToken: getAccessToken,
});

export async function setToggle(
  module: string,
  enabled: boolean,
): Promise<{ module: string; enabled: boolean; domain: string }> {
  const token = getAccessToken();
  const res = await fetch(
    `${config.apiBase}${API_PREFIX.toggles}/v1/toggles/${encodeURIComponent(module)}`,
    {
      method: "PUT",
      headers: {
        "Content-Type": "application/json",
        Accept: "application/json",
        ...(token ? { Authorization: `Bearer ${token}` } : {}),
      },
      body: JSON.stringify({ enabled }),
    },
  );
  if (!res.ok) {
    const body = await res.text().catch(() => "");
    throw new Error(`Failed to update toggle ${module}: HTTP ${res.status} ${body.slice(0, 200)}`);
  }
  toggleClient.invalidate();
  return (await res.json()) as { module: string; enabled: boolean; domain: string };
}
