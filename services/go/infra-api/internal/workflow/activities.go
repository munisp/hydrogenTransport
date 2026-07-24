package workflow

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/zap"
)

// Activities implements the Temporal activities backing the incident/dispatch
// workflows. All activities are idempotent Postgres writes against the infra
// schema; the pool comes from DATABASE_URL (see cmd/server/main.go).
type Activities struct {
	pool *pgxpool.Pool
	log  *zap.Logger
}

// NewActivities builds the activity set registered on the worker.
func NewActivities(pool *pgxpool.Pool, log *zap.Logger) *Activities {
	return &Activities{pool: pool, log: log}
}

// SetIncidentInProgress marks an open/acknowledged incident in_progress when
// its incident-response workflow picks up the leak signal.
func (a *Activities) SetIncidentInProgress(ctx context.Context, incidentID string) error {
	tag, err := a.pool.Exec(ctx, `
		UPDATE infra.incidents SET status = 'in_progress'
		WHERE id = $1 AND status IN ('open','acknowledged')`, incidentID)
	if err != nil {
		return err
	}
	a.log.Info("incident set in_progress",
		zap.String("incident", incidentID), zap.Int64("rows", tag.RowsAffected()))
	return nil
}

// EscalateIncident bumps the severity of an unresolved incident one level
// (low -> medium -> high -> critical) after the acknowledgement deadline.
// Already-resolved (or missing) incidents are a no-op.
func (a *Activities) EscalateIncident(ctx context.Context, incidentID string) error {
	var from, to string
	err := a.pool.QueryRow(ctx, `
		WITH old AS (SELECT severity FROM infra.incidents WHERE id = $1)
		UPDATE infra.incidents i
		SET severity = CASE i.severity
			WHEN 'low' THEN 'medium'
			WHEN 'medium' THEN 'high'
			WHEN 'high' THEN 'critical'
			ELSE i.severity END
		FROM old
		WHERE i.id = $1 AND i.status != 'resolved'
		RETURNING old.severity, i.severity`, incidentID).Scan(&from, &to)
	if errors.Is(err, pgx.ErrNoRows) {
		a.log.Info("escalation skipped; incident gone or already resolved",
			zap.String("incident", incidentID))
		return nil
	}
	if err != nil {
		return err
	}
	a.log.Warn("incident severity escalated",
		zap.String("incident", incidentID), zap.String("from", from), zap.String("to", to))
	return nil
}

// MarkIncidentResolved closes the incident when the workflow receives the
// resolve signal. Idempotent with the HTTP resolve handler.
func (a *Activities) MarkIncidentResolved(ctx context.Context, incidentID string) error {
	tag, err := a.pool.Exec(ctx, `
		UPDATE infra.incidents SET status = 'resolved'
		WHERE id = $1 AND status != 'resolved'`, incidentID)
	if err != nil {
		return err
	}
	a.log.Info("incident resolved",
		zap.String("incident", incidentID), zap.Int64("rows", tag.RowsAffected()))
	return nil
}

// RequeueDispatchJob returns an unaccepted job to the queue (status
// 'requeued') so dispatch can reassign it.
func (a *Activities) RequeueDispatchJob(ctx context.Context, jobID string) error {
	tag, err := a.pool.Exec(ctx, `
		UPDATE infra.dispatch_jobs SET status = 'requeued'
		WHERE id = $1 AND status = 'assigned'`, jobID)
	if err != nil {
		return err
	}
	a.log.Warn("dispatch job requeued",
		zap.String("job", jobID), zap.Int64("rows", tag.RowsAffected()))
	return nil
}
