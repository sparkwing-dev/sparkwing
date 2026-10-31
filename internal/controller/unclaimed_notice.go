package controller

import (
	"fmt"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/engineid"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/pkg/match"
)

func unclaimedNotice(view store.RunView) *engineid.Notice {
	unit, run := view.Waiting, view.Snapshot.Run
	if unit == nil || view.Snapshot.StoreNow.Sub(unit.Since) < claimAttentionGrace {
		return nil
	}
	if unit.CreditHold != "" {
		return &engineid.Notice{Kind: engineid.NoticeCreditHold, Reason: unit.CreditHold}
	}
	requires := run.Request.Request.Options.Requires.Value
	if unit.Kind == capability.UnitNode {
		for _, node := range view.Snapshot.Nodes {
			if string(node.Ref.NodeID) == unit.NodeID {
				requires = node.Execution.Requires.Value
				break
			}
		}
	}
	remedy := ""
	if len(requires) > 0 {
		remedy = "; required runner labels: " + strings.Join(requires, ", ")
		labels := match.SelfAsserted(requires)
		for i, label := range labels {
			labels[i] = "--label '" + strings.ReplaceAll(label, "'", "'\\''") + "'"
		}
		if len(labels) > 0 {
			remedy += "; ask a team administrator to add a matching runner with `sparkwing runners add --runner NAME --profile PROFILE " + strings.Join(labels, " ") + " --repos REPOSITORY --session-stdin`"
		}
	}
	if unit.NoEligible != "" {
		return &engineid.Notice{Kind: engineid.NoticeNoEligibleRunner, Reason: unit.NoEligible + remedy}
	}
	switch store.RequestPlacement(run.Request.Request) {
	case nodeproto.PlacementRunnerHosted, nodeproto.PlacementLauncher, nodeproto.PlacementGitHubActions:
	default:
		return nil
	}
	what := map[capability.UnitKind]string{capability.UnitTrigger: "the run's trigger", capability.UnitPlan: "the run's plan", capability.UnitEval: "evaluation of job " + unit.NodeID, capability.UnitNode: "job " + unit.NodeID}[unit.Kind]
	reason := fmt.Sprintf("no runner or launcher has claimed %s yet; check that a matching runner (or the Cloud launcher) is online", what) + remedy
	if !run.Request.Provenance.DefaultBranchWork() {
		reason += "; if targeting a pool holding reserved secrets, it needs verified default-branch work: push to the repository's default branch through the GitHub App, or select a pool without reserved secrets and start a new run"
	}
	return &engineid.Notice{Kind: engineid.NoticeUnclaimed, Reason: reason}
}
