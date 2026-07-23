import { useState, type FormEvent } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { TriangleAlert } from "lucide-react";
import { useAuth } from "../../auth/AuthContext";
import { acceptDispatchJob, listDispatchJobs, reportIncident } from "../../api/infra";
import {
  Button,
  Card,
  CardContent,
  CardHeader,
  CardTitle,
  ErrorState,
  Field,
  Input,
  PageHeader,
  Select,
  Spinner,
  StatusBadge,
} from "../../components/ui";
import { formatDateTime } from "../../lib/format";

/**
 * mobile-app (web surface) — the driver's view that the Expo app also exposes:
 * assigned dispatch jobs and one-tap incident reporting.
 */
export default function DriverPortalPage() {
  const { identity } = useAuth();
  const queryClient = useQueryClient();

  const jobs = useQuery({
    queryKey: ["infra", "dispatch", "jobs", "mine", identity.username],
    queryFn: () => listDispatchJobs({ driver_sub: identity.username }),
    refetchInterval: 30_000,
  });

  const accept = useMutation({
    mutationFn: acceptDispatchJob,
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ["infra", "dispatch"] }),
  });

  return (
    <div>
      <PageHeader
        title="Driver Portal"
        description="Assigned jobs from dispatch and quick incident reporting — the same surfaces shipped in the Expo driver app."
      />

      <div className="grid grid-cols-1 gap-4 xl:grid-cols-3">
        <Card className="xl:col-span-2">
          <CardHeader>
            <CardTitle>My assigned jobs</CardTitle>
          </CardHeader>
          <CardContent className="space-y-3">
            {jobs.isLoading ? (
              <Spinner />
            ) : jobs.isError ? (
              <ErrorState error={jobs.error} onRetry={() => jobs.refetch()} />
            ) : (jobs.data ?? []).length === 0 ? (
              <p className="py-8 text-center text-sm text-stone-400">
                No jobs assigned to {identity.username} right now.
              </p>
            ) : (
              (jobs.data ?? []).map((job) => (
                <div
                  key={job.id}
                  className="flex flex-wrap items-center justify-between gap-3 rounded-lg border border-stone-200 px-4 py-3"
                >
                  <div>
                    <p className="text-sm font-medium text-stone-800">
                      Route {job.route}
                      {job.vehicle_id ? ` · Bus ${job.vehicle_id.slice(0, 8)}` : ""}
                    </p>
                    <p className="mt-0.5 text-xs text-stone-500">
                      Starts {formatDateTime(job.starts_at)}
                    </p>
                  </div>
                  <div className="flex items-center gap-2">
                    <StatusBadge status={job.status} />
                    {job.status === "assigned" ? (
                      <Button
                        variant="secondary"
                        busy={accept.isPending && accept.variables === job.id}
                        onClick={() => accept.mutate(job.id)}
                      >
                        Accept
                      </Button>
                    ) : null}
                  </div>
                </div>
              ))
            )}
          </CardContent>
        </Card>

        <IncidentReportCard />
      </div>
    </div>
  );
}

function IncidentReportCard() {
  const [type, setType] = useState("breakdown");
  const [severity, setSeverity] = useState("medium");
  const [busId, setBusId] = useState("");
  const [description, setDescription] = useState("");
  const [done, setDone] = useState(false);

  const report = useMutation({
    mutationFn: reportIncident,
    onSuccess: () => {
      setDescription("");
      setBusId("");
      setDone(true);
      setTimeout(() => setDone(false), 4000);
    },
  });

  function submit(e: FormEvent) {
    e.preventDefault();
    report.mutate({
      type,
      severity,
      bus_id: busId.trim() || null,
      description,
    });
  }

  return (
    <Card>
      <CardHeader>
        <CardTitle>Report an incident</CardTitle>
      </CardHeader>
      <CardContent>
        <form className="space-y-4" onSubmit={submit}>
          <Field label="Type">
            <Select value={type} onChange={(e) => setType(e.target.value)}>
              <option value="breakdown">Breakdown</option>
              <option value="leak">H2 leak suspicion</option>
              <option value="collision">Collision</option>
              <option value="station_fault">Station fault</option>
              <option value="security">Security</option>
            </Select>
          </Field>
          <Field label="Severity">
            <Select value={severity} onChange={(e) => setSeverity(e.target.value)}>
              <option value="low">Low</option>
              <option value="medium">Medium</option>
              <option value="high">High</option>
              <option value="critical">Critical</option>
            </Select>
          </Field>
          <Field label="Bus ID (optional)">
            <Input value={busId} onChange={(e) => setBusId(e.target.value)} placeholder="uuid" />
          </Field>
          <Field label="Description">
            <Input required value={description} onChange={(e) => setDescription(e.target.value)} placeholder="What happened?" />
          </Field>
          <Button type="submit" variant="danger" busy={report.isPending} className="w-full">
            <TriangleAlert className="h-4 w-4" aria-hidden />
            Send report
          </Button>
          {report.isError ? (
            <p className="text-xs text-red-700">
              {report.error instanceof Error ? report.error.message : "Report failed"}
            </p>
          ) : null}
          {done ? (
            <p className="text-xs font-medium text-teal-700">
              Incident reported — dispatch has been notified.
            </p>
          ) : null}
        </form>
      </CardContent>
    </Card>
  );
}
