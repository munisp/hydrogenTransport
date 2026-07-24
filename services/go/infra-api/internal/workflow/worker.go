package workflow

import (
	"github.com/jackc/pgx/v5/pgxpool"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/worker"
	"go.uber.org/zap"
)

// StartWorker dials Temporal at host and starts a worker on TaskQueue with
// the incident-response and dispatch workflows plus their activities. It
// returns a stop function; a nil stop function (and nil error) is returned
// when host is empty so callers can run without Temporal in minimal dev
// environments. The worker starts in the background; dial/poll failures are
// retried by the SDK, so an unreachable server never blocks HTTP serving.
func StartWorker(host string, pool *pgxpool.Pool, log *zap.Logger) (stop func(), err error) {
	if host == "" {
		log.Warn("TEMPORAL_HOST not set; incident/dispatch workflow worker not started")
		return func() {}, nil
	}
	c, err := client.Dial(client.Options{
		HostPort:  host,
		Namespace: "default",
	})
	if err != nil {
		return nil, err
	}
	w := worker.New(c, TaskQueue, worker.Options{})
	w.RegisterWorkflow(IncidentResponseWorkflow)
	w.RegisterWorkflow(DispatchWorkflow)
	w.RegisterActivity(NewActivities(pool, log))
	if err := w.Start(); err != nil {
		c.Close()
		return nil, err
	}
	log.Info("temporal worker started", zap.String("host", host), zap.String("taskQueue", TaskQueue))
	return func() {
		w.Stop()
		c.Close()
		log.Info("temporal worker stopped")
	}, nil
}
