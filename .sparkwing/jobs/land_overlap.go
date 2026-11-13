package jobs

import (
	"time"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// safety: queued fast checks keep the deadlines they had while they shared
// review's job, independent of the affected-test deadline beside them.
const (
	queuedFastTimeout           = 45 * time.Minute
	queuedFastNoProgressTimeout = 30 * time.Minute
)

type queuedLandChecks struct {
	sparkwing.Base
	sparkwing.Produces[reviewRecord]
	args   ReviewArgs
	branch string
	Checks *queuedChecks
	Head   sparkwing.Ref[landHead]
}

func (j *queuedLandChecks) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	// safety: shared fail-fast ownership joins and drains tests when review fails.
	w.FailurePolicy(sparkwing.FailFast)
	if _, err := (&affectedPushTestsJob{queued: true, Checks: j.Checks, Head: j.Head}).Work(w); err != nil {
		return nil, err
	}
	return (&reviewJob{args: j.args, branch: j.branch, Checks: j.Checks, Head: j.Head}).Work(w)
}
