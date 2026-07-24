package workflow

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/suite"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/testsuite"
)

type WorkflowTestSuite struct {
	suite.Suite
	testsuite.WorkflowTestSuite
}

func TestWorkflowTestSuite(t *testing.T) {
	suite.Run(t, new(WorkflowTestSuite))
}

// registerActivityStubs registers no-op activities under the production
// activity names so tests can set expectations with env.OnActivity(name, ...).
func registerActivityStubs(env *testsuite.TestWorkflowEnvironment) {
	stub := func(context.Context, string) error { return nil }
	for _, name := range []string{
		"SetIncidentInProgress",
		"EscalateIncident",
		"MarkIncidentResolved",
		"RequeueDispatchJob",
	} {
		env.RegisterActivityWithOptions(stub, activity.RegisterOptions{Name: name})
	}
}

// countCalls registers a counting stub for one activity and returns the
// counter, for asserting the activity was never invoked.
func countCalls(env *testsuite.TestWorkflowEnvironment, name string) *int32 {
	var calls int32
	env.RegisterActivityWithOptions(func(context.Context, string) error {
		atomic.AddInt32(&calls, 1)
		return nil
	}, activity.RegisterOptions{Name: name})
	return &calls
}

// Incident happy path: leak -> in_progress, ack cancels escalation, resolve
// closes the workflow.
func (s *WorkflowTestSuite) TestIncidentResponseWorkflow_HappyPath() {
	env := s.NewTestWorkflowEnvironment()
	registerActivityStubs(env)
	escalations := countCalls(env, "EscalateIncident")

	env.OnActivity("SetIncidentInProgress", mock.Anything, mock.Anything).Return(nil).Once()
	env.OnActivity("MarkIncidentResolved", mock.Anything, mock.Anything).Return(nil).Once()

	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow(SignalLeakDetected, map[string]any{"severity": "high"})
	}, 0)
	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow(SignalIncidentAcknowledged, map[string]any{"incident_id": "x"})
	}, time.Minute)
	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow(SignalIncidentResolved, map[string]any{"incident_id": "x"})
	}, 2*time.Minute)

	env.ExecuteWorkflow(IncidentResponseWorkflow)
	s.True(env.IsWorkflowCompleted())
	s.NoError(env.GetWorkflowError())
	s.Equal(int32(0), atomic.LoadInt32(escalations), "escalation must not fire on the happy path")
	env.AssertExpectations(s.T())
}

// Incident escalation: no ack within IncidentEscalationTimeout escalates
// severity exactly once; a later resolve still closes the workflow.
func (s *WorkflowTestSuite) TestIncidentResponseWorkflow_EscalationTimeout() {
	env := s.NewTestWorkflowEnvironment()
	registerActivityStubs(env)
	escalations := countCalls(env, "EscalateIncident")

	env.OnActivity("SetIncidentInProgress", mock.Anything, mock.Anything).Return(nil).Once()
	env.OnActivity("MarkIncidentResolved", mock.Anything, mock.Anything).Return(nil).Once()

	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow(SignalLeakDetected, map[string]any{"severity": "medium"})
	}, 0)
	// Resolve after the escalation deadline without ever acking.
	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow(SignalIncidentResolved, map[string]any{"incident_id": "x"})
	}, IncidentEscalationTimeout+5*time.Minute)

	env.ExecuteWorkflow(IncidentResponseWorkflow)
	s.True(env.IsWorkflowCompleted())
	s.NoError(env.GetWorkflowError())
	s.Equal(int32(1), atomic.LoadInt32(escalations), "unacknowledged incident must escalate exactly once")
	env.AssertExpectations(s.T())
}

// Dispatch happy path: assignment accepted before the timeout closes the
// workflow without a requeue.
func (s *WorkflowTestSuite) TestDispatchWorkflow_Accepted() {
	env := s.NewTestWorkflowEnvironment()
	registerActivityStubs(env)
	requeues := countCalls(env, "RequeueDispatchJob")

	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow(SignalJobAssigned, map[string]any{"driver_id": "driver-1"})
	}, 0)
	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow(SignalJobAccepted, map[string]any{"job_id": "x"})
	}, time.Minute)

	env.ExecuteWorkflow(DispatchWorkflow)
	s.True(env.IsWorkflowCompleted())
	s.NoError(env.GetWorkflowError())
	s.Equal(int32(0), atomic.LoadInt32(requeues), "accepted job must not be requeued")
	env.AssertExpectations(s.T())
}

// Dispatch timeout: no accept within DispatchAcceptTimeout requeues the job
// and the workflow waits for the next assignment.
func (s *WorkflowTestSuite) TestDispatchWorkflow_TimeoutRequeue() {
	env := s.NewTestWorkflowEnvironment()
	registerActivityStubs(env)
	requeues := countCalls(env, "RequeueDispatchJob")

	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow(SignalJobAssigned, map[string]any{"driver_id": "driver-1"})
	}, 0)
	// After the requeue, reassign and cancel to let the workflow finish.
	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow(SignalJobAssigned, map[string]any{"driver_id": "driver-2"})
	}, DispatchAcceptTimeout+5*time.Minute)
	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow(SignalJobCancelled, nil)
	}, DispatchAcceptTimeout+6*time.Minute)

	env.ExecuteWorkflow(DispatchWorkflow)
	s.True(env.IsWorkflowCompleted())
	s.NoError(env.GetWorkflowError())
	s.Equal(int32(1), atomic.LoadInt32(requeues), "unaccepted job must be requeued exactly once")
	env.AssertExpectations(s.T())
}

// Dispatch cancelled while awaiting assignment closes immediately.
func (s *WorkflowTestSuite) TestDispatchWorkflow_CancelledBeforeAssignment() {
	env := s.NewTestWorkflowEnvironment()
	registerActivityStubs(env)

	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow(SignalJobCancelled, nil)
	}, time.Minute)

	env.ExecuteWorkflow(DispatchWorkflow)
	s.True(env.IsWorkflowCompleted())
	s.NoError(env.GetWorkflowError())
	env.AssertExpectations(s.T())
}
