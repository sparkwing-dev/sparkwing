package controller

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/engine"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func (s *EngineController) unclaimedNotice(ctx context.Context, view store.RunView) (*engine.Notice, error) {
	unit, run := view.Waiting, view.Snapshot.Run
	if unit == nil || s.Store == nil {
		return nil, nil
	}
	switch store.RequestPlacement(run.Request.Request) {
	case nodeproto.PlacementRunnerHosted, nodeproto.PlacementLauncher, nodeproto.PlacementGitHubActions:
	default:
		return nil, nil
	}
	waited := view.Snapshot.StoreNow.Sub(unit.Since)
	if waited < claimAttentionGrace {
		return nil, nil
	}
	tenant, err := s.Store.ForTeam(ctx, run.Team)
	if err != nil {
		return nil, err
	}
	if eligible, err := tenant.UnitHasEligibleClaimant(ctx, run.ID, *unit, agentOnlineWithin); eligible || err != nil {
		return nil, err
	}
	hint := "Sparkwing Cloud's launcher takes this run's plan, evaluation and job units; check that the launcher is running and can place work for team " + string(run.Team)
	if run.Dispatch != string(runspec.DispatchController) {
		hint = "a self-hosted or GitHub Actions runner with the matching claim grant and capacity must poll for it"
		if len(unit.Needs) > 0 {
			hint += " (needs " + strings.Join(unit.Needs, ", ") + ")"
		}
	}
	what := map[capability.UnitKind]string{capability.UnitTrigger: "the run's trigger", capability.UnitPlan: "the run's plan", capability.UnitEval: "evaluation of job " + unit.NodeID, capability.UnitNode: "job " + unit.NodeID}[unit.Kind]
	return &engine.Notice{Kind: engine.NoticeUnclaimed, Reason: fmt.Sprintf("no eligible runner or launcher for %s after %s", what, waited.Round(time.Second)), Hint: hint}, nil
}
