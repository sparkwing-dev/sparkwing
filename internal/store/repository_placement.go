package store

import (
	"fmt"
	"slices"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

// safety: only an executable run whose intake sealed repository proof takes repository placement; an App runner
// pin and a placement list conflict rather than one silently winning, and a per-run --runner choice replaces the list.
func sealRepositoryPlacement(input *runspec.Request, policy *runspec.RepositoryPlacement, owner GitHubAppRunner, provenance runspec.Provenance) error {
	if policy == nil || provenance.ProvenRepository() == "" || input.Mode != runspec.IntakeExecute || input.Burst != nil {
		return nil
	}
	if owner != "" && owner != GitHubAppRunnerAnywhere {
		return authorityError(apierr.BadRequest, fmt.Sprintf("the GitHub App runner setting %s conflicts with the placement of pipeline %s in .sparkwing/sparkwing.yaml; set the runner to anywhere or remove the placement", owner, input.Pipeline))
	}
	if runner := input.Options.Runner; runner.Set && runner.Value.Kind != runspec.RunnerAny {
		return nil
	}
	if err := policy.Validate(); err != nil {
		return authorityError(apierr.BadRequest, "repository placement: "+err.Error())
	}
	input.Placement = policy
	return nil
}

// safety: planning and evaluation keep their own placement; only jobs follow a repository list.
func repositoryPlacementApplies(request runspec.Request, unit UnitRef) bool {
	return unit.Kind == capability.UnitNode && len(request.Placement.ForJob(string(unit.NodeID))) > 0
}

// safety: an override naming no plan job fails acceptance unless a generator may still add it; inline jobs
// stay on their run host, so a placement cannot move them.
func checkPlacementJobs(policy *runspec.RepositoryPlacement, pipeline string, jobs []nodeproto.JobSpec) error {
	if policy == nil {
		return nil
	}
	generates := slices.ContainsFunc(jobs, func(job nodeproto.JobSpec) bool { return len(job.Closures.Generate) > 0 })
	for _, override := range policy.Jobs {
		i := slices.IndexFunc(jobs, func(job nodeproto.JobSpec) bool { return job.ID == override.Job })
		switch {
		case i < 0 && !generates:
			return authorityError(apierr.PlanInvalid, fmt.Sprintf("the placement for job %s of pipeline %s in .sparkwing/sparkwing.yaml at default-branch commit %s names no job in this plan", override.Job, pipeline, policy.Revision))
		case i >= 0 && jobs[i].Inline && len(override.Prefer) > 0:
			return authorityError(apierr.PlanInvalid, fmt.Sprintf("job %s is inline and stays on its run host; remove its placement from .sparkwing/sparkwing.yaml", override.Job))
		}
	}
	return nil
}
