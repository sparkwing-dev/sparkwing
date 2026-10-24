package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
)

func policyOutcome(p runstate.OwnerPolicyResult) runstate.NodeState {
	switch p.Kind {
	case runstate.PolicyAdmissionSkip:
		return runstate.NodeSkipped
	case runstate.PolicyAdmissionFail, runstate.PolicyQueueTimeout, runstate.PolicyApprovalFail:
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
	if policy.Kind == runstate.PolicyQueueTimeout && result.Reason != FailureQueueTimeout {
		return errors.New("queue timeout requires its policy reason")
	}
	if policy.Kind == runstate.PolicyApprovalBypass || policy.Kind == runstate.PolicyApprovalFail {
		var job nodeproto.JobSpec
		if json.Unmarshal(row.Spec, &job) != nil || job.Approval == nil || len(job.Closures.SkipIf) == 0 || policy.Kind == runstate.PolicyApprovalBypass && result.Reason != "skip_if" {
			return errors.New("approval bypass requires its declared skip predicate")
		}
	}
	return nil
}

func applyRunOwnerPolicyTx(ctx context.Context, tx *storeTx, team Team, row RunRow, change runstate.RunTransition, now time.Time) error {
	if err := checkOwnerTx(ctx, tx, team, change.Actor, row.ID); err != nil {
		return err
	}
	policy := change.OwnerPolicy
	if policy == nil || policy.Generation != change.Actor.Owner.Generation || row.Spec == nil || row.CancelRequestedAt != nil {
		return holderRefusal()
	}
	if policy.Kind != runstate.PolicyAdmissionSkip && policy.Kind != runstate.PolicyAdmissionFail && policy.Kind != runstate.PolicyQueueTimeout {
		return errors.New("invalid plan admission policy result")
	}
	snapshot, err := readSnapshotTx(ctx, tx, team, row.ID)
	if err != nil {
		return err
	}
	for _, node := range snapshot.Nodes {
		if node.Status == runstate.StatusDone {
			continue
		}
		if node.Status != runstate.StatusPending || node.ReadyAt != nil || node.ClaimedBy != "" {
			return errors.New("plan admission refusal requires unreleased work")
		}
		_, err := transitionNodeStateTx(ctx, tx, team, nodeTransition{
			Ref: node.Ref, Actor: change.Actor, To: policyOutcome(*policy), Cause: causeOwnerPolicy,
			Extra: transitionExtra{OwnerPolicy: &runstate.OwnerPolicyResult{Kind: policy.Kind, Generation: node.Generation}, Result: &runstate.NodeResult{Outcome: policyOutcome(*policy), Reason: change.Reason, MaskedError: change.MaskedError}},
		})
		if err != nil {
			return err
		}
	}
	_, err = tx.execContext(ctx, `UPDATE nodes SET expansion=CASE WHEN outcome IN ('success','memoized') THEN 'failed' ELSE 'none' END WHERE team=? AND run_id=? AND expansion='expected'`, team, row.ID)
	return err
}
