// Package runner is the seam between the orchestrator's dispatch loop
// and per-node execution.
package runner

import (
	"context"
	"errors"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// Runner executes one pipeline node to a terminal outcome.
type Runner interface {
	RunNode(ctx context.Context, req Request) Result
}

// DownstreamCoordinator marks a runner whose execution process owns the
// node's cache and concurrency lifecycle. The upstream orchestrator must not
// wrap it with a second coordinator. Only operator-constructed runners may
// implement this marker; candidate plans never select runner implementations.
type DownstreamCoordinator interface {
	CoordinatesDownstream()
}

// PlanValidationRequest binds the core-generated canonical plan projection to
// the authoritative run identity that produced it.
type PlanValidationRequest struct {
	RunContext       sparkwing.RunContext
	Args             map[string]string
	ProfileName      string
	ProfileIsLocal   bool
	Projection       []byte
	ProjectionDigest string
}

// PlanValidator lets a trusted runner reject a candidate-authored projection
// before it is persisted or any cache lookup, inline routing, or node dispatch
// can produce side effects.
type PlanValidator interface {
	ValidatePlan(context.Context, PlanValidationRequest) error
}

// RunFinalizer lets a runner close durable execution state after every
// admitted run. FinalizeRun must be idempotent because callers may retry it
// after losing the response.
type RunFinalizer interface {
	FinalizeRun(context.Context, RunFinalizationRequest) error
}

type terminalFinalizationError struct{ error }

func (terminalFinalizationError) TerminalFinalization() {}

func TerminalFinalizationError(err error) error {
	if err == nil {
		return nil
	}
	return terminalFinalizationError{error: err}
}

func IsTerminalFinalizationError(err error) bool {
	var terminal interface{ TerminalFinalization() }
	return errors.As(err, &terminal)
}

type RunFinalizationRequest struct {
	RunID   string
	Outcome sparkwing.Outcome
	Error   error
}

// LabelAdvertiser is an optional interface a Runner can implement to
// expose the labels it satisfies. The orchestrator consults it when
// evaluating Job.WhenRunner: a job whose WhenRunner labels cannot be
// matched by the active runner is silently skipped at dispatch time.
// Runners that do not implement this interface are treated as
// matching anything, preserving the pre-WhenRunner behavior.
type LabelAdvertiser interface {
	AdvertisedLabels() []string
}

// Request is the work handed to a runner. Cluster runners ignore the
// in-process fields (Node, Delegate) and reconstruct pod-side.
type Request struct {
	RunID    string
	NodeID   string
	Pipeline string
	Args     map[string]string
	Git      *sparkwing.Git
	Trigger  sparkwing.TriggerInfo

	// Profile and PlanDigest bind this node to the same canonical projection
	// accepted before dispatch. Trusted runners reject a mismatch.
	ProfileName    string
	ProfileIsLocal bool
	PlanDigest     string

	// Node is set for in-process runners; cluster runners leave nil.
	Node *sparkwing.JobNode

	// Delegate mirrors log lines; in-process only.
	Delegate sparkwing.Logger

	// ReleaseWorkerSlot and ReacquireWorkerSlot let a node that blocks
	// on concurrency admission give back its MaxParallel worker slot for
	// the duration of the wait, so a queue of waiters can't starve other
	// ready nodes. ReacquireWorkerSlot reports false if the run was
	// cancelled while re-acquiring. Both are nil for cluster runners and
	// when no worker cap is configured; callers must nil-check.
	ReleaseWorkerSlot   func()
	ReacquireWorkerSlot func() bool
}

// Result is the terminal outcome. Err is non-nil only when Outcome
// is Failed.
type Result struct {
	Outcome sparkwing.Outcome
	Output  any
	Err     error
}
