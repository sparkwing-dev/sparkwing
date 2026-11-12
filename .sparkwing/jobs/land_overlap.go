package jobs

import (
	"time"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// safety: splitting fast checks must retain their former review-job deadline.
const queuedFastNoProgressTimeout = 30 * time.Minute

type queuedLandChecks struct {
	sparkwing.Base
	sparkwing.Produces[reviewRecord]
	args   ReviewArgs
	branch string
}

func (j *queuedLandChecks) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	// safety: shared fail-fast ownership joins and drains tests when review fails.
	w.FailurePolicy(sparkwing.FailFast)
	if _, err := (&affectedPushTestsJob{queued: true}).Work(w); err != nil {
		return nil, err
	}
	return (&reviewJob{args: j.args, branch: j.branch}).Work(w)
}
