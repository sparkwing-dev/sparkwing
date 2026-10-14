package orchestrator

import (
	"context"
	"fmt"
)

type planAdmissionEvictedError struct {
	groupName    string
	policy       string
	supersededBy string
}

func (e *planAdmissionEvictedError) Error() string {
	if e.supersededBy != "" {
		return fmt.Sprintf("admission on %q lost under %s: superseded by run %s; run `sparkwing queue` to inspect the queue", e.groupName, e.policy, e.supersededBy)
	}
	return fmt.Sprintf("plan concurrency group %q: admission evicted; run `sparkwing queue` to inspect the queue", e.groupName)
}

func appendPlanEvent(ctx context.Context, backends Backends, runID, kind string, payload []byte) {
	if backends.State == nil {
		return
	}
	noteEvent(ctx, backends.State, runID, "", kind, payload)
}
