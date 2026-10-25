package store

import (
	"encoding/json"
	"errors"

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
)

func policyOutcome(p runstate.OwnerPolicyResult) runstate.NodeState {
	switch p.Kind {
	case runstate.PolicyApprovalFail:
		return runstate.NodeFailed
	case runstate.PolicyApprovalBypass:
		return runstate.NodeSkipped
	default:
		return ""
	}
}

func checkNodeOwnerPolicy(row runstate.NodeRow, change nodeTransition) error {
	policy, result := change.Extra.OwnerPolicy, change.Extra.Result
	if policy == nil || policy.Generation < 0 || policy.Generation != row.Generation || row.ReadyAt != nil || result == nil || policyOutcome(*policy) == "" || policyOutcome(*policy) != change.To {
		return holderRefusal()
	}
	var job nodeproto.JobSpec
	if json.Unmarshal(row.Spec, &job) != nil || job.Approval == nil || len(job.Closures.SkipIf) == 0 || policy.Kind == runstate.PolicyApprovalBypass && result.Reason != "skip_if" {
		return errors.New("approval policy requires its declared skip predicate")
	}
	return nil
}
