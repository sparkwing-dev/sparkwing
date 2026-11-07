package store

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
)

// safety: the list only narrows where jobs run, so it is sealed whatever the provenance, a child stays inside
// the list of the parent job that started it, and any runner pin conflicts with a list rather than one winning.
func sealRepositoryPlacement(input *runspec.Request, own, parent *runspec.RepositoryPlacement, parentJob string, owner GitHubAppRunner) error {
	if input.Mode != runspec.IntakeExecute {
		return nil
	}
	policy, err := runspec.BindToParent(own, parent, parentJob)
	if err != nil {
		return authorityError(apierr.BadRequest, fmt.Sprintf("the placement of child pipeline %s %v; narrow it to tiers its parent allows", input.Pipeline, err))
	}
	if !policy.Active() {
		return nil
	}
	conflict := func(setting string) error {
		return authorityError(apierr.BadRequest, fmt.Sprintf("%s conflicts with the placement of pipeline %s from .sparkwing/sparkwing.yaml on the default branch; remove one of them", setting, input.Pipeline))
	}
	switch runner := input.Options.Runner; {
	case input.Burst != nil:
		return conflict("burst routing")
	case owner != "" && owner != GitHubAppRunnerAnywhere:
		return conflict("the GitHub App runner setting " + string(owner))
	case runner.Set && runner.Value.Kind != runspec.RunnerAny:
		return conflict(strings.TrimSpace("the runner selection " + string(runner.Value.Kind) + " " + runner.Value.Value))
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

// safety: runs at plan and expansion acceptance alike. An override for a job a plan lacks is inert, because
// the list binds job IDs; an inline job stays on its run host, and a list no tier of which can meet the job's
// operating-system requirements would leave the job waiting forever, so both refuse the job.
func checkJobPlacementTx(ctx context.Context, tx *storeTx, team Team, row runstate.NodeRow) error {
	var raw []byte
	if err := tx.QueryRowContext(ctx, `SELECT request_json FROM runs WHERE team=? AND id=?`, team, row.Ref.RunID).Scan(&raw); err != nil {
		return err
	}
	var sealed struct {
		Request struct {
			Placement *runspec.RepositoryPlacement `json:"placement"`
		} `json:"request"`
	}
	if err := json.Unmarshal(raw, &sealed); err != nil {
		return err
	}
	return checkJobPlacement(sealed.Request.Placement, row)
}

func checkJobPlacement(policy *runspec.RepositoryPlacement, row runstate.NodeRow) error {
	tiers := policy.ForJob(string(row.Ref.NodeID))
	if len(tiers) == 0 {
		return nil
	}
	var job nodeproto.JobSpec
	if err := json.Unmarshal(row.Spec, &job); err != nil {
		return err
	}
	if job.Inline {
		return authorityError(apierr.PlanInvalid, fmt.Sprintf("job %s is inline and stays on its run host; remove its placement from .sparkwing/sparkwing.yaml", row.Ref.NodeID))
	}
	requires := row.Execution.Requires.Value
	if !slices.ContainsFunc(tiers, func(tier runspec.RepositoryTier) bool { return tier.MaySatisfy(requires) }) {
		names := make([]string, len(tiers))
		for i, tier := range tiers {
			names[i] = tier.String()
		}
		return authorityError(apierr.PlanInvalid, fmt.Sprintf("job %s requires %s, which no tier of its placement (%s) can satisfy", row.Ref.NodeID, strings.Join(requires, " "), strings.Join(names, "; ")))
	}
	return nil
}
