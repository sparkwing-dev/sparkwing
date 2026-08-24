package runner

import (
	"errors"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// NodeTerminal reports whether a node row carries the outcome its
// executor wrote. Every runner that watches a node execute somewhere
// else -- a Kubernetes Job, a local child process -- asks this before
// trusting the row, because a row that is merely present says nothing
// about whether the work finished.
func NodeTerminal(n *store.Node) bool {
	return n != nil && n.Status == "done" && n.Outcome != ""
}

// ResultFromNode maps a terminal node row onto the Result the
// dispatch loop consumes. The executing process is the authority on
// its own outcome, so a runner that supervises one reports what the
// row says rather than inferring an outcome from how the process
// exited.
//
// Output stays raw []byte: unmarshaling here would erase the typed
// shape and break Ref[T].Get downstream.
func ResultFromNode(n *store.Node) Result {
	res := Result{Outcome: sparkwing.Outcome(n.Outcome)}
	if n.Error != "" {
		res.Err = errors.New(n.Error)
	}
	if len(n.Output) > 0 {
		res.Output = n.Output
	}
	return res
}
