// Package workflow wraps Temporal workflow signalling for incident/dispatch
// workflows (SPEC §3.8). When TEMPORAL_HOST is unset a logging no-op signaler
// is used so the service still runs in minimal dev environments.
package workflow

import (
	"context"
	"strings"

	"go.temporal.io/sdk/client"
	"go.uber.org/zap"
)

// Signaler signals running Temporal workflows (incident response, dispatch).
type Signaler interface {
	// Signal sends a signal to the workflow with the given ID (any run).
	Signal(ctx context.Context, workflowID, signalName string, payload any) error
	Close()
}

// NewSignaler dials Temporal at host (e.g. "temporal:7233"); falls back to a
// no-op signaler when host is empty or dialing fails.
func NewSignaler(host string, log *zap.Logger) Signaler {
	if host == "" {
		log.Warn("TEMPORAL_HOST not set; workflow signals will be logged but not sent")
		return &noopSignaler{log: log}
	}
	c, err := client.Dial(client.Options{
		HostPort:  host,
		Namespace: "default",
	})
	if err != nil {
		log.Error("temporal dial failed; falling back to no-op signaler", zap.String("host", host), zap.Error(err))
		return &noopSignaler{log: log}
	}
	log.Info("temporal client connected", zap.String("host", host))
	return &temporalSignaler{c: c, log: log}
}

type temporalSignaler struct {
	c   client.Client
	log *zap.Logger
}

func (t *temporalSignaler) Signal(ctx context.Context, workflowID, signalName string, payload any) error {
	// Start the workflow if it is not running yet: handlers signal right
	// after creating the incident/job, so SignalWithStart makes the signal
	// the workflow's first event instead of failing on a missing run.
	if wf, ok := workflowForID(workflowID); ok {
		_, err := t.c.SignalWithStartWorkflow(ctx, workflowID, signalName, payload,
			client.StartWorkflowOptions{
				ID:        workflowID,
				TaskQueue: TaskQueue,
			}, wf)
		if err != nil {
			t.log.Error("temporal signal-with-start failed",
				zap.String("workflow", workflowID), zap.String("signal", signalName), zap.Error(err))
			return err
		}
		t.log.Info("temporal signal sent (workflow started if needed)",
			zap.String("workflow", workflowID), zap.String("signal", signalName))
		return nil
	}
	if err := t.c.SignalWorkflow(ctx, workflowID, "", signalName, payload); err != nil {
		t.log.Error("temporal signal failed",
			zap.String("workflow", workflowID), zap.String("signal", signalName), zap.Error(err))
		return err
	}
	t.log.Info("temporal signal sent",
		zap.String("workflow", workflowID), zap.String("signal", signalName))
	return nil
}

func (t *temporalSignaler) Close() { t.c.Close() }

// workflowForID maps a workflow ID prefix to its workflow function; the
// second return value is false for unknown prefixes.
func workflowForID(workflowID string) (any, bool) {
	switch {
	case strings.HasPrefix(workflowID, IncidentWorkflowIDPrefix):
		return IncidentResponseWorkflow, true
	case strings.HasPrefix(workflowID, DispatchWorkflowIDPrefix):
		return DispatchWorkflow, true
	default:
		return nil, false
	}
}

type noopSignaler struct {
	log *zap.Logger
}

func (n *noopSignaler) Signal(_ context.Context, workflowID, signalName string, payload any) error {
	n.log.Info("workflow signal (no-op signaler)",
		zap.String("workflow", workflowID), zap.String("signal", signalName), zap.Any("payload", payload))
	return nil
}

func (n *noopSignaler) Close() {}
