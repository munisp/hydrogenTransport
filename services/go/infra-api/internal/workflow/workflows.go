package workflow

import (
	"strings"
	"time"

	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"
)

// TaskQueue is the Temporal task queue the infra-api worker polls (SPEC §3.8:
// dispatch + incident workflows).
const TaskQueue = "infra-workflows"

// Workflow ID prefixes. HTTP handlers address workflows as
// "<prefix><entity uuid>" (see internal/handlers).
const (
	IncidentWorkflowIDPrefix = "incident-"
	DispatchWorkflowIDPrefix = "dispatch-"
)

// Signal names shared with internal/handlers (must stay in sync).
const (
	SignalLeakDetected         = "leak-detected"
	SignalIncidentAcknowledged = "incident-acknowledged"
	SignalIncidentResolved     = "incident-resolved"
	SignalJobAssigned          = "job-assigned"
	SignalJobAccepted          = "job-accepted"
	SignalJobCancelled         = "job-cancelled"
)

// Timeouts governing workflow behaviour.
const (
	// IncidentEscalationTimeout is how long an incident may stay
	// unacknowledged before its severity is escalated.
	IncidentEscalationTimeout = 15 * time.Minute
	// DispatchAcceptTimeout is how long a driver has to accept an assigned
	// job before it is requeued.
	DispatchAcceptTimeout = 10 * time.Minute
)

// activityOptions returns the common activity options for infra workflows:
// short-lived Postgres writes, retried a few times before failing the
// workflow.
func activityOptions(ctx workflow.Context) workflow.Context {
	return workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout: 30 * time.Second,
		RetryPolicy: &temporal.RetryPolicy{
			InitialInterval:    time.Second,
			BackoffCoefficient: 2,
			MaximumAttempts:    5,
		},
	})
}

// IncidentResponseWorkflow (workflow ID "incident-<uuid>") drives the
// leak-detection incident lifecycle:
//
//  1. awaits the "leak-detected" signal (delivered by IngestLeak), then marks
//     the incident in_progress via activity;
//  2. waits for acknowledgement — if no "incident-acknowledged" signal
//     arrives within IncidentEscalationTimeout, severity is escalated via
//     activity (Postgres update + log);
//  3. closes when the "incident-resolved" signal arrives, marking the
//     incident resolved (idempotent with the HTTP resolve handler).
func IncidentResponseWorkflow(ctx workflow.Context) error {
	log := workflow.GetLogger(ctx)
	incidentID := strings.TrimPrefix(workflow.GetInfo(ctx).WorkflowExecution.ID, IncidentWorkflowIDPrefix)
	actCtx := activityOptions(ctx)

	// 1. Await the initial leak signal (payload: safety.leak.detected event).
	var leak map[string]any
	workflow.GetSignalChannel(ctx, SignalLeakDetected).Receive(ctx, &leak)
	log.Info("leak signal received", "incident", incidentID, "severity", leak["severity"])
	if err := workflow.ExecuteActivity(actCtx, "SetIncidentInProgress", incidentID).Get(ctx, nil); err != nil {
		return err
	}

	// 2./3. Await ack (with escalation deadline) and resolve.
	ackCh := workflow.GetSignalChannel(ctx, SignalIncidentAcknowledged)
	resolveCh := workflow.GetSignalChannel(ctx, SignalIncidentResolved)
	acknowledged := false
	escalated := false
	resolved := false
	for !resolved {
		sel := workflow.NewSelector(ctx)
		sel.AddReceive(resolveCh, func(c workflow.ReceiveChannel, _ bool) {
			c.Receive(ctx, nil)
			resolved = true
		})
		if !acknowledged {
			sel.AddReceive(ackCh, func(c workflow.ReceiveChannel, _ bool) {
				c.Receive(ctx, nil)
				acknowledged = true
			})
			if !escalated {
				timer := workflow.NewTimer(ctx, IncidentEscalationTimeout)
				sel.AddFuture(timer, func(workflow.Future) {
					log.Warn("incident unacknowledged; escalating severity",
						"incident", incidentID, "timeout", IncidentEscalationTimeout.String())
					if err := workflow.ExecuteActivity(actCtx, "EscalateIncident", incidentID).Get(ctx, nil); err != nil {
						log.Error("escalation activity failed", "incident", incidentID, "error", err)
					}
					escalated = true
				})
			}
		}
		sel.Select(ctx)
	}
	if err := workflow.ExecuteActivity(actCtx, "MarkIncidentResolved", incidentID).Get(ctx, nil); err != nil {
		return err
	}
	log.Info("incident workflow closed", "incident", incidentID)
	return nil
}

// DispatchWorkflow (workflow ID "dispatch-<uuid>") tracks one dispatch job:
//
//  1. awaits the "job-assigned" signal (delivered by CreateDispatchJob);
//  2. waits for "job-accepted" — on DispatchAcceptTimeout without an accept
//     the job is requeued via activity (status -> requeued) and the workflow
//     returns to step 1 awaiting a fresh assignment;
//  3. closes on "job-accepted" or "job-cancelled".
func DispatchWorkflow(ctx workflow.Context) error {
	log := workflow.GetLogger(ctx)
	jobID := strings.TrimPrefix(workflow.GetInfo(ctx).WorkflowExecution.ID, DispatchWorkflowIDPrefix)
	actCtx := activityOptions(ctx)

	assignedCh := workflow.GetSignalChannel(ctx, SignalJobAssigned)
	acceptedCh := workflow.GetSignalChannel(ctx, SignalJobAccepted)
	cancelledCh := workflow.GetSignalChannel(ctx, SignalJobCancelled)

	for {
		// 1. Await assignment (or cancellation while unassigned).
		var assignment map[string]any
		cancelled := false
		sel := workflow.NewSelector(ctx)
		sel.AddReceive(assignedCh, func(c workflow.ReceiveChannel, _ bool) {
			c.Receive(ctx, &assignment)
		})
		sel.AddReceive(cancelledCh, func(c workflow.ReceiveChannel, _ bool) {
			c.Receive(ctx, nil)
			cancelled = true
		})
		sel.Select(ctx)
		if cancelled {
			log.Info("dispatch job cancelled before assignment", "job", jobID)
			return nil
		}
		log.Info("dispatch job assigned", "job", jobID, "driver", assignment["driver_id"])

		// 2. Await accept / cancel / timeout.
		accepted := false
		sel = workflow.NewSelector(ctx)
		sel.AddReceive(acceptedCh, func(c workflow.ReceiveChannel, _ bool) {
			c.Receive(ctx, nil)
			accepted = true
		})
		sel.AddReceive(cancelledCh, func(c workflow.ReceiveChannel, _ bool) {
			c.Receive(ctx, nil)
			cancelled = true
		})
		timer := workflow.NewTimer(ctx, DispatchAcceptTimeout)
		timedOut := false
		sel.AddFuture(timer, func(workflow.Future) { timedOut = true })
		sel.Select(ctx)

		switch {
		case accepted:
			log.Info("dispatch job accepted", "job", jobID)
			return nil
		case cancelled:
			log.Info("dispatch job cancelled", "job", jobID)
			return nil
		case timedOut:
			log.Warn("dispatch job not accepted in time; requeuing",
				"job", jobID, "timeout", DispatchAcceptTimeout.String())
			if err := workflow.ExecuteActivity(actCtx, "RequeueDispatchJob", jobID).Get(ctx, nil); err != nil {
				return err
			}
			// Loop back and await the next assignment.
		}
	}
}
