// Package runner is the seam between the orchestrator's dispatch loop
// and per-node execution.
package runner

import (
	"context"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// Runner executes one pipeline node to a terminal outcome.
type Runner interface {
	RunNode(ctx context.Context, req Request) Result
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
	Node *sparkwing.Node

	// Delegate mirrors log lines; in-process only.
	Delegate sparkwing.Logger
}

// Result is the terminal outcome. Err is non-nil only when Outcome
// is Failed.
type Result struct {
	Outcome sparkwing.Outcome
	Output  any
	Err     error
}
