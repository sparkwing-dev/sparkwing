package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
)

// OwnerPolicyKind identifies a decision made before execution is released.
type OwnerPolicyKind string

// Owner policy outcomes distinguish admission refusal from approval bypass.
const (
	PolicyAdmissionSkip  OwnerPolicyKind = "admission_skip"
	PolicyAdmissionFail  OwnerPolicyKind = "admission_fail"
	PolicyQueueTimeout   OwnerPolicyKind = "admission_queue_timeout"
	PolicyApprovalBypass OwnerPolicyKind = "approval_bypass"
)

// OwnerPolicyResult binds a declared decision to its captured work generation.
type OwnerPolicyResult struct {
	Kind       OwnerPolicyKind
	Generation int64
}

func (p OwnerPolicyResult) outcome() NodeState {
	switch p.Kind {
	case PolicyAdmissionSkip:
		return NodeSkipped
	case PolicyAdmissionFail, PolicyQueueTimeout:
		return NodeFailed
	case PolicyApprovalBypass:
		return NodeSuccess
	default:
		return ""
	}
}

func checkNodeOwnerPolicy(row NodeRow, change NodeTransition) error {
	policy, result := change.Extra.OwnerPolicy, change.Extra.Result
	if policy == nil || policy.Generation < 0 || policy.Generation != row.Generation || row.ReadyAt != nil || result == nil || policy.outcome() == "" || policy.outcome() != change.To {
		return holderRefusal()
	}
	if policy.Kind == PolicyQueueTimeout && result.Reason != FailureQueueTimeout {
		return errors.New("queue timeout requires its policy reason")
	}
	if policy.Kind == PolicyApprovalBypass {
		var job nodeproto.JobSpec
		if json.Unmarshal(row.Spec, &job) != nil || job.Approval == nil || len(job.Closures.SkipIf) == 0 || result.Reason != "skip_if" {
			return errors.New("approval bypass requires its declared skip predicate")
		}
	}
	return nil
}

func applyRunOwnerPolicyTx(ctx context.Context, tx *storeTx, team Team, row RunRow, change RunTransition, now time.Time) error {
	if err := checkOwnerTx(ctx, tx, team, change.Actor, row.ID, now); err != nil {
		return err
	}
	policy := change.OwnerPolicy
	if policy == nil || policy.Generation != change.Actor.Owner.Generation || row.Spec == nil || row.CancelRequestedAt != nil {
		return holderRefusal()
	}
	want := RunFailed
	if policy.Kind == PolicyAdmissionSkip {
		want = RunSuccess
	} else if policy.Kind != PolicyAdmissionFail && policy.Kind != PolicyQueueTimeout {
		return errors.New("invalid plan admission policy result")
	}
	if change.To != want {
		return errors.New("plan verdict disagrees with admission policy")
	}
	snapshot, err := readSnapshotTx(ctx, tx, team, row.ID)
	if err != nil {
		return err
	}
	for _, node := range snapshot.Nodes {
		if node.Status == StatusDone {
			continue
		}
		if node.Status != StatusPending || node.ReadyAt != nil || node.ClaimedBy != "" {
			return errors.New("plan admission refusal requires unreleased work")
		}
		_, err := transitionNodeStateTx(ctx, tx, team, NodeTransition{
			Ref: node.Ref, Actor: change.Actor, To: policy.outcome(), Cause: CauseOwnerPolicy,
			Extra: TransitionExtra{OwnerPolicy: &OwnerPolicyResult{Kind: policy.Kind, Generation: node.Generation}, Result: &NodeResult{Outcome: policy.outcome(), Reason: change.Reason, MaskedError: change.MaskedError}},
		}, tx.now())
		if err != nil {
			return err
		}
	}
	return nil
}
