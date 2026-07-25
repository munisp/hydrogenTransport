import { useMemo, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Inbox, KeyRound, UserPlus, UserX } from "lucide-react";
import {
  approveOnboarding,
  createUser,
  listOnboarding,
  listUsers,
  rejectOnboarding,
  resetUserPassword,
  setUserEnabled,
  updateUserRoles,
  type AdminUser,
  type OnboardingRequest,
} from "../../api/admin";
import { useAuth } from "../../auth/AuthContext";
import {
  Badge,
  Button,
  Card,
  EmptyState,
  ErrorState,
  Field,
  Input,
  Modal,
  Select,
  Skeleton,
  StatusBadge,
  Table,
  Td,
  Th,
} from "../../components/ui";

/** Realm roles assignable from this console (persona map in admin-api). */
const KNOWN_ROLES = ["platform-admin", "operator", "dispatcher", "driver", "citizen", "gov-viewer"];

/**
 * Users & Onboarding tab. The onboarding request queue (approve/reject) is
 * available to operator+; account management below requires platform-admin
 * (the API enforces both splits).
 */
export default function UsersTab() {
  const { hasRole } = useAuth();
  const isAdmin = hasRole("platform-admin");
  return (
    <div className="space-y-10">
      <OnboardingQueue />
      {isAdmin ? <UserManagement /> : null}
    </div>
  );
}

// ---- Onboarding queue (operator+) ----------------------------------------------

function OnboardingQueue() {
  const queryClient = useQueryClient();
  const [statusFilter, setStatusFilter] = useState<string>("pending");
  const [actionError, setActionError] = useState<string | null>(null);

  const requests = useQuery({
    queryKey: ["admin", "onboarding", statusFilter],
    queryFn: () => listOnboarding({ status: statusFilter as OnboardingRequest["status"] }),
    refetchInterval: 30_000,
  });

  const decide = useMutation({
    mutationFn: ({ id, approve }: { id: string; approve: boolean }) =>
      approve ? approveOnboarding(id) : rejectOnboarding(id),
    onSuccess: () => {
      setActionError(null);
      queryClient.invalidateQueries({ queryKey: ["admin", "onboarding"] });
    },
    onError: (err) => setActionError(err instanceof Error ? err.message : "Decision failed"),
  });

  return (
    <section aria-labelledby="ob-queue">
      <div className="mb-3 flex flex-wrap items-center justify-between gap-3">
        <div>
          <h2 id="ob-queue" className="text-sm font-semibold text-stone-800">
            Onboarding requests
          </h2>
          <p className="text-xs text-stone-500">
            Public intake from /welcome. Approving provisions the Keycloak account with the persona role.
          </p>
        </div>
        <div className="w-40">
          <Select
            aria-label="Filter by status"
            value={statusFilter}
            onChange={(e) => setStatusFilter(e.target.value)}
          >
            <option value="pending">pending</option>
            <option value="approved">approved</option>
            <option value="completed">completed</option>
            <option value="rejected">rejected</option>
            <option value="">all</option>
          </Select>
        </div>
      </div>

      {actionError ? (
        <p role="alert" className="mb-3 rounded-xl border border-red-200 bg-red-50 px-4 py-3 text-sm text-red-800">
          {actionError}
        </p>
      ) : null}

      {requests.isLoading ? (
        <Card className="p-5" aria-busy="true">
          <div className="space-y-3">
            {[0, 1, 2].map((i) => (
              <Skeleton key={i} className="h-9 w-full" />
            ))}
          </div>
        </Card>
      ) : requests.isError ? (
        <ErrorState error={requests.error} onRetry={() => requests.refetch()} />
      ) : (requests.data ?? []).length === 0 ? (
        <EmptyState
          icon={<Inbox className="h-6 w-6" />}
          title={`No ${statusFilter || ""} onboarding requests`}
          body="New submissions from the public /welcome intake appear here in real time."
        />
      ) : (
        <Card>
          <Table>
            <thead>
              <tr>
                <Th>Name</Th>
                <Th>Persona</Th>
                <Th>Organisation</Th>
                <Th>Submitted</Th>
                <Th>Status</Th>
                <Th className="text-right">Actions</Th>
              </tr>
            </thead>
            <tbody>
              {(requests.data ?? []).map((r) => (
                <tr key={r.id}>
                  <Td>
                    <p className="font-medium text-stone-800">{r.display_name}</p>
                    <p className="text-xs text-stone-500">{r.email}</p>
                  </Td>
                  <Td>
                    <Badge tone="teal">{r.persona}</Badge>
                  </Td>
                  <Td>{r.org || "—"}</Td>
                  <Td className="whitespace-nowrap text-xs">
                    {new Date(r.created_at).toLocaleString()}
                  </Td>
                  <Td>
                    <StatusBadge status={r.status} />
                  </Td>
                  <Td className="text-right">
                    {r.status === "pending" ? (
                      <div className="flex justify-end gap-2">
                        <Button
                          className="px-2.5 py-1 text-xs"
                          busy={decide.isPending && decide.variables?.id === r.id && decide.variables?.approve}
                          disabled={decide.isPending}
                          onClick={() => decide.mutate({ id: r.id, approve: true })}
                        >
                          Approve
                        </Button>
                        <Button
                          variant="secondary"
                          className="px-2.5 py-1 text-xs"
                          busy={decide.isPending && decide.variables?.id === r.id && !decide.variables?.approve}
                          disabled={decide.isPending}
                          onClick={() => decide.mutate({ id: r.id, approve: false })}
                        >
                          Reject
                        </Button>
                      </div>
                    ) : (
                      <span className="text-xs text-stone-400">
                        {r.decided_by ? `by ${r.decided_by}` : "—"}
                      </span>
                    )}
                  </Td>
                </tr>
              ))}
            </tbody>
          </Table>
        </Card>
      )}
    </section>
  );
}

// ---- User management (platform-admin) -------------------------------------------

function UserManagement() {
  const queryClient = useQueryClient();
  const [q, setQ] = useState("");
  const [role, setRole] = useState("");
  const [createOpen, setCreateOpen] = useState(false);
  const [notice, setNotice] = useState<string | null>(null);
  const [actionError, setActionError] = useState<string | null>(null);

  const users = useQuery({
    queryKey: ["admin", "users", q, role],
    queryFn: () => listUsers({ q, role }),
  });

  const run = useMutation({
    mutationFn: async (action: { kind: string; user: AdminUser; role?: string }) => {
      const { kind, user, role: r } = action;
      switch (kind) {
        case "disable":
          return setUserEnabled(user.id, false);
        case "enable":
          return setUserEnabled(user.id, true);
        case "reset":
          return resetUserPassword(user.id);
        case "add-role":
          return updateUserRoles(user.id, { add: [r!] });
        case "remove-role":
          return updateUserRoles(user.id, { remove: [r!] });
        default:
          return undefined;
      }
    },
    onSuccess: (_data, action) => {
      setActionError(null);
      if (action.kind === "reset") {
        setNotice(`Password reset email sent to ${action.user.email}.`);
      }
      queryClient.invalidateQueries({ queryKey: ["admin", "users"] });
    },
    onError: (err) => setActionError(err instanceof Error ? err.message : "Action failed"),
  });

  return (
    <section aria-labelledby="user-mgmt">
      <div className="mb-3 flex flex-wrap items-end justify-between gap-3">
        <div>
          <h2 id="user-mgmt" className="text-sm font-semibold text-stone-800">
            User accounts
          </h2>
          <p className="text-xs text-stone-500">
            Keycloak realm accounts. Role changes apply on the next token refresh.
          </p>
        </div>
        <Button onClick={() => setCreateOpen(true)}>
          <UserPlus className="h-4 w-4" aria-hidden /> Create user
        </Button>
      </div>

      <div className="mb-3 flex flex-wrap gap-3">
        <div className="w-full sm:w-64">
          <Input
            type="search"
            aria-label="Search users"
            placeholder="Search name or email…"
            value={q}
            onChange={(e) => setQ(e.target.value)}
          />
        </div>
        <div className="w-44">
          <Select aria-label="Filter by role" value={role} onChange={(e) => setRole(e.target.value)}>
            <option value="">all roles</option>
            {KNOWN_ROLES.map((r) => (
              <option key={r} value={r}>
                {r}
              </option>
            ))}
          </Select>
        </div>
      </div>

      {notice ? (
        <p role="status" className="mb-3 rounded-xl border border-teal-200 bg-teal-50 px-4 py-3 text-sm text-teal-800">
          {notice}
        </p>
      ) : null}
      {actionError ? (
        <p role="alert" className="mb-3 rounded-xl border border-red-200 bg-red-50 px-4 py-3 text-sm text-red-800">
          {actionError}
        </p>
      ) : null}

      {users.isLoading ? (
        <Card className="p-5" aria-busy="true">
          <div className="space-y-3">
            {[0, 1, 2, 3].map((i) => (
              <Skeleton key={i} className="h-9 w-full" />
            ))}
          </div>
        </Card>
      ) : users.isError ? (
        <ErrorState error={users.error} onRetry={() => users.refetch()} />
      ) : (users.data ?? []).length === 0 ? (
        <EmptyState
          icon={<UserX className="h-6 w-6" />}
          title="No users match"
          body="Adjust the search or role filter, or create a new account."
        />
      ) : (
        <Card>
          <Table>
            <thead>
              <tr>
                <Th>User</Th>
                <Th>Roles</Th>
                <Th>Status</Th>
                <Th className="text-right">Actions</Th>
              </tr>
            </thead>
            <tbody>
              {(users.data ?? []).map((u) => (
                <UserRow
                  key={u.id}
                  user={u}
                  busy={run.isPending && run.variables?.user.id === u.id}
                  onAction={(kind, r) => run.mutate({ kind, user: u, role: r })}
                />
              ))}
            </tbody>
          </Table>
        </Card>
      )}

      {createOpen ? (
        <CreateUserModal
          onClose={() => setCreateOpen(false)}
          onCreated={(email) => {
            setCreateOpen(false);
            setNotice(`Account created for ${email} — verification email sent.`);
            queryClient.invalidateQueries({ queryKey: ["admin", "users"] });
          }}
        />
      ) : null}
    </section>
  );
}

function UserRow({
  user,
  busy,
  onAction,
}: {
  user: AdminUser;
  busy: boolean;
  onAction: (kind: string, role?: string) => void;
}) {
  const [rolePick, setRolePick] = useState("");
  const name = [user.first_name, user.last_name].filter(Boolean).join(" ") || user.username;
  const assignable = useMemo(
    () => KNOWN_ROLES.filter((r) => !user.roles.includes(r)),
    [user.roles],
  );

  return (
    <tr>
      <Td>
        <p className="font-medium text-stone-800">{name}</p>
        <p className="text-xs text-stone-500">{user.email}</p>
      </Td>
      <Td>
        <div className="flex flex-wrap items-center gap-1">
          {user.roles.length === 0 ? <span className="text-xs text-stone-400">none</span> : null}
          {user.roles.map((r) => (
            <span key={r} className="inline-flex items-center gap-1">
              <Badge tone="teal">{r}</Badge>
              <button
                type="button"
                aria-label={`Revoke ${r} from ${user.email}`}
                title={`Revoke ${r}`}
                disabled={busy}
                onClick={() => onAction("remove-role", r)}
                className="rounded text-stone-300 hover:text-red-600 focus-visible:outline focus-visible:outline-2 focus-visible:outline-accent disabled:opacity-40"
              >
                ×
              </button>
            </span>
          ))}
        </div>
      </Td>
      <Td>
        <StatusBadge status={user.enabled ? "active" : "offline"} />
      </Td>
      <Td>
        <div className="flex flex-wrap items-center justify-end gap-2">
          <div className="flex items-center gap-1">
            <Select
              aria-label={`Assign role to ${user.email}`}
              value={rolePick}
              onChange={(e) => setRolePick(e.target.value)}
              className="w-36 px-2 py-1 text-xs"
            >
              <option value="">assign role…</option>
              {assignable.map((r) => (
                <option key={r} value={r}>
                  {r}
                </option>
              ))}
            </Select>
            <Button
              variant="secondary"
              className="px-2 py-1 text-xs"
              disabled={busy || !rolePick}
              onClick={() => {
                onAction("add-role", rolePick);
                setRolePick("");
              }}
            >
              Add
            </Button>
          </div>
          <Button
            variant="ghost"
            className="px-2 py-1 text-xs"
            disabled={busy}
            title="Send password reset email"
            onClick={() => onAction("reset")}
          >
            <KeyRound className="h-3.5 w-3.5" aria-hidden /> Reset
          </Button>
          <Button
            variant={user.enabled ? "ghost" : "secondary"}
            className="px-2 py-1 text-xs"
            disabled={busy}
            onClick={() => onAction(user.enabled ? "disable" : "enable")}
          >
            {user.enabled ? "Disable" : "Enable"}
          </Button>
        </div>
      </Td>
    </tr>
  );
}

function CreateUserModal({
  onClose,
  onCreated,
}: {
  onClose: () => void;
  onCreated: (email: string) => void;
}) {
  const [email, setEmail] = useState("");
  const [displayName, setDisplayName] = useState("");
  const [roles, setRoles] = useState<string[]>([]);
  const [error, setError] = useState<string | null>(null);

  const create = useMutation({
    mutationFn: () =>
      createUser({ email: email.trim(), display_name: displayName.trim(), roles }),
    onSuccess: () => onCreated(email.trim()),
    onError: (err) => setError(err instanceof Error ? err.message : "Create failed"),
  });

  const valid = email.includes("@") && displayName.trim().length > 0;

  return (
    <Modal title="Create user" onClose={onClose}>
      <form
        onSubmit={(e) => {
          e.preventDefault();
          if (valid && !create.isPending) create.mutate();
        }}
        className="space-y-4"
      >
        <Field label="Email address" hint="Also used as the username.">
          <Input
            type="email"
            value={email}
            onChange={(e) => setEmail(e.target.value)}
            required
            autoFocus
          />
        </Field>
        <Field label="Display name">
          <Input value={displayName} onChange={(e) => setDisplayName(e.target.value)} required />
        </Field>
        <fieldset>
          <legend className="mb-1 text-xs font-medium text-stone-600">Initial roles</legend>
          <div className="flex flex-wrap gap-2">
            {KNOWN_ROLES.map((r) => {
              const on = roles.includes(r);
              return (
                <button
                  key={r}
                  type="button"
                  aria-pressed={on}
                  onClick={() =>
                    setRoles((cur) => (on ? cur.filter((x) => x !== r) : [...cur, r]))
                  }
                  className={
                    on
                      ? "rounded-full bg-teal-50 px-3 py-1 text-xs font-medium text-teal-800 ring-1 ring-inset ring-teal-300"
                      : "rounded-full bg-surface-sunken px-3 py-1 text-xs text-stone-600 ring-1 ring-inset ring-stone-200 hover:ring-stone-300"
                  }
                >
                  {r}
                </button>
              );
            })}
          </div>
        </fieldset>
        {error ? (
          <p role="alert" className="rounded-lg border border-red-200 bg-red-50 px-3 py-2 text-xs text-red-800">
            {error}
          </p>
        ) : null}
        <p className="text-xs text-stone-400">
          The account is created with a temporary password; the user receives a
          verify-email + set-password actions email.
        </p>
        <div className="flex justify-end gap-2">
          <Button type="button" variant="secondary" onClick={onClose}>
            Cancel
          </Button>
          <Button type="submit" busy={create.isPending} disabled={!valid}>
            Create account
          </Button>
        </div>
      </form>
    </Modal>
  );
}
