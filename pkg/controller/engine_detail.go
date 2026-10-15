package controller

import (
	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/engine"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func engineRunDetail(view store.RunView) controllerwire.RunDetail {
	run := view.Snapshot.Run
	detail := controllerwire.RunDetail{
		ID: run.ID, Number: run.Number, Team: string(run.Team), Status: string(run.State), StateSeq: run.StateSeq,
		Inputs: runspec.DisplayRequest(run.Request), Jobs: []controllerwire.JobDetail{}, Attempts: []controllerwire.AttemptDetail{},
		Logs: append([]blob.LogSubject{}, view.Logs...), Notices: []engine.Notice{},
		StartedAt: run.StartedAt, FinishedAt: run.FinishedAt, CancelRequestedAt: run.CancelRequestedAt,
	}
	if run.Spec != nil {
		detail.Inputs = runspec.DisplaySpec(*run.Spec)
		identity := run.Spec.Resolved.Engine
		detail.Engine = &identity
	}
	for _, row := range view.Snapshot.Nodes {
		detail.Jobs = append(detail.Jobs, engineJob(row))
	}
	for _, attempt := range view.Attempts {
		started := attempt.StartedAt
		detail.Attempts = append(detail.Attempts, controllerwire.AttemptDetail{
			JobID: attempt.Identity.NodeID, Attempt: attempt.Identity.Attempt, Generation: attempt.Identity.Generation,
			Outcome: string(attempt.Outcome), Reason: attempt.FailureReason, Executor: attempt.ExecutorKind,
			StartedAt: &started, FinishedAt: attempt.FinishedAt,
		})
	}
	// safety: retries retain the original seal; the current run owns the additional lineage.
	detail.Inputs.Lineage = run.Lineage
	return detail
}
