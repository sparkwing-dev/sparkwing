// Package runner is the seam between the orchestrator's dispatch loop
// and per-node execution.
package runner

import (
	"context"
	"time"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// Runner executes one pipeline node to a terminal outcome.
type Runner interface {
	RunNode(ctx context.Context, req Request) Result
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

	// Usage is the kernel's exit-time accounting for the process that
	// ran the node, when a runner supervised one. Nil for runners that
	// do not own a process (in-process execution, a pod the
	// orchestrator only polls). It is exact where per-second sampling
	// is not, which is why it is carried separately from the sampled
	// metrics; the capacity fold consumes it.
	Usage *ResourceUsage
}

// ResourceUsage is what one node's process actually cost.
type ResourceUsage struct {
	// CPUTime is user plus system time across the process tree.
	CPUTime time.Duration
	// MaxRSSBytes is peak resident set size, normalized to bytes.
	MaxRSSBytes int64

	// Wall is how long that process existed, spawn to reap, measured by
	// the runner that owns it. It is the span CPUTime was drawn over, and
	// the only span that matches it: a node's own started_at/finished_at
	// are stamped inside the process, after runtime and plan startup and
	// before teardown, so dividing this CPU by that window reports cores
	// the machine never gave -- a millisecond of work would price at host
	// capacity. Zero when the runner did not time the process.
	Wall time.Duration
}
