package controller

import (
	"fmt"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/engine"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func unclaimedNotice(view store.RunView) *engine.Notice {
	unit, run := view.Waiting, view.Snapshot.Run
	if unit == nil || view.Snapshot.StoreNow.Sub(unit.Since) < claimAttentionGrace {
		return nil
	}
	if unit.CreditHold != "" {
		return &engine.Notice{Kind: engine.NoticeCreditHold, Reason: unit.CreditHold}
	}
	switch store.RequestPlacement(run.Request.Request) {
	case nodeproto.PlacementRunnerHosted, nodeproto.PlacementLauncher, nodeproto.PlacementGitHubActions:
	default:
		return nil
	}
	what := map[capability.UnitKind]string{capability.UnitTrigger: "the run's trigger", capability.UnitPlan: "the run's plan", capability.UnitEval: "evaluation of job " + unit.NodeID, capability.UnitNode: "job " + unit.NodeID}[unit.Kind]
	return &engine.Notice{Kind: engine.NoticeUnclaimed, Reason: fmt.Sprintf("no runner or launcher has claimed %s yet; check that a matching runner (or the Cloud launcher) is online", what)}
}
