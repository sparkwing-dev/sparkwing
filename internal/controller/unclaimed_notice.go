package controller

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/engineid"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
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
	if slices.ContainsFunc(view.PlacementWaits, func(wait store.PlacementWait) bool {
		return unit.Kind == capability.UnitNode && wait.JobID == unit.NodeID
	}) {
		return nil
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

// safety: notices render the store's projection, so the CLI and the console show the same tiers and times.
func placementNotices(view store.RunView) []engineid.Notice {
	var out []engineid.Notice
	for _, wait := range view.PlacementWaits {
		parts := make([]string, len(wait.Tiers))
		actions := false
		for i, tier := range wait.Tiers {
			var state []string
			if tier.Unavailable != "" {
				state = append(state, tier.Unavailable)
			} else if tier.OpensIn > 0 {
				state = append(state, "opens in "+tier.OpensIn.Round(time.Second).String())
			}
			if len(tier.Live) > 0 {
				state = append(state, strings.Join(tier.Live, ", ")+" live")
			}
			if tier.Busy > 0 {
				state = append(state, fmt.Sprintf("%d busy", tier.Busy))
			}
			if tier.OnDemand {
				state = append(state, "launches on demand")
			}
			parts[i] = tier.Tier.String() + " (" + strings.Join(state, ", ") + ")"
			actions = actions || tier.Tier.Target == runspec.PlacementGitHubActions
		}
		revision := wait.Revision
		if len(revision) > 12 {
			revision = revision[:12]
		}
		hint := "Tiers come from .sparkwing/sparkwing.yaml on the default branch at " + revision + "; a later tier opens when every earlier tier with a live runner has passed its hold."
		if actions {
			hint += " A GitHub Actions tier spends the repository account's Actions minutes."
		}
		out = append(out, engineid.Notice{Kind: engineid.NoticePlacementHold, Reason: "job " + wait.JobID + " waits for placement: " + strings.Join(parts, "; then "), Hint: hint})
	}
	return out
}
