package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

func pendingApprovalPredicate(row NodeRow) bool {
	var job nodeproto.JobSpec
	return row.Status == StatusPending && row.ReadyAt == nil && row.ClaimedBy == "" && json.Unmarshal(row.Spec, &job) == nil && capability.EvaluationReadsDeclarations(job)
}

func scheduleApprovalEvaluationTx(ctx context.Context, tx *storeTx, team Team, ref controllerwire.NodeRef, actor Actor) (bool, bool, error) {
	row, err := readNodeTx(ctx, tx, team, ref)
	if err != nil || !pendingApprovalPredicate(row) {
		return false, false, err
	}
	var status string
	err = tx.QueryRowContext(ctx, `SELECT status FROM eval_units WHERE team=? AND run_id=? AND source_node_id=? AND source_generation=?`, team, ref.RunID, ref.NodeID, row.Generation).Scan(&status)
	if err == nil {
		if status == "failed" {
			_, err = transitionNodeStateTx(ctx, tx, team, nodeTransition{Ref: ref, Actor: actor, To: NodeFailed, Cause: causeOwnerPolicy, Extra: transitionExtra{OwnerPolicy: &OwnerPolicyResult{Kind: PolicyApprovalFail, Generation: row.Generation}, Result: &NodeResult{Outcome: NodeFailed, Reason: "approval evaluator lost"}}})
			return true, err == nil, err
		}
		return true, false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return true, false, err
	}
	released, err := releaseEvaluationTx(ctx, tx, team, ref, actor)
	if err != nil || released.Waiting {
		return true, false, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO eval_units(team,run_id,source_node_id,source_generation,status,owner_generation) VALUES(?,?,?,?,'pending',?)`, team, ref.RunID, ref.NodeID, row.Generation, actor.Owner.Generation); err != nil {
		return true, false, err
	}
	return true, false, nil
}

func checkApprovalEvaluationTx(ctx context.Context, tx *storeTx, team Team, row NodeRow, change nodeTransition) error {
	policy := change.Extra.OwnerPolicy
	if change.Actor.Kind != ActorRunHost || change.Actor.Owner.Kind != OwnerEval || !pendingApprovalPredicate(row) || change.Cause != CauseApprovalOpen && (change.Cause != causeOwnerPolicy || policy == nil || policy.Kind != PolicyApprovalBypass && policy.Kind != PolicyApprovalFail) {
		return holderRefusal()
	}
	return expansionAuthorityTx(ctx, tx, team, ExpansionSubmission{Source: row.Ref, SourceGeneration: row.Generation, Actor: change.Actor})
}

func finishApprovalEvaluationTx(ctx context.Context, tx *storeTx, team Team, row NodeRow, change nodeTransition) error {
	if err := finishExpansionBillingTx(ctx, tx, team, ExpansionSubmission{Source: row.Ref, SourceGeneration: row.Generation, Actor: change.Actor}); err != nil {
		return err
	}
	if err := expansionAuthorityTx(ctx, tx, team, ExpansionSubmission{Source: row.Ref, SourceGeneration: row.Generation, Actor: change.Actor}); err != nil {
		return err
	}
	lease := change.Actor.Owner
	now := tx.now(ctx).UnixNano()
	result, err := tx.ExecContext(ctx, `UPDATE eval_units SET status='done',finished_at=?,claimed_by=NULL,capability_prefix='',lease_expires_at=NULL,claimed_at=NULL WHERE team=? AND run_id=? AND source_node_id=? AND source_generation=? AND generation=? AND claimed_by=? AND capability_prefix=? AND status IN ('claimed','running') AND lease_expires_at>? AND EXISTS(SELECT 1 FROM leases l WHERE l.name=eval_units.owner_lease_name AND l.generation=eval_units.owner_generation AND l.until>?)`, now, team, row.Ref.RunID, row.Ref.NodeID, row.Generation, lease.Generation, lease.Holder, change.Actor.CapabilityPrefix, now, now)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return holderRefusal()
	}
	return nil
}

func evaluationSourceTx(ctx context.Context, tx *storeTx, team Team, unit UnitRef) error {
	row, err := readNodeTx(ctx, tx, team, controllerwire.NodeRef{RunID: unit.RunID, NodeID: unit.NodeID})
	if err != nil {
		return err
	}
	if row.Generation != runspec.ClaimGeneration(unit.SourceGeneration) {
		return holderRefusal()
	}
	if evaluationSourceCurrent(row) {
		return nil
	}
	return holderRefusal()
}

func evaluationSourceCurrent(row NodeRow) bool {
	return pendingApprovalPredicate(row) || row.Status == StatusDone && scheduleSucceeded(string(row.Outcome)) && row.Expansion == ExpansionExpected
}

func evaluationSourceSQL(q *claimSQL, node string) string {
	spec := q.blob(node + `.spec_json`)
	approval := node + `.status='pending' AND ` + node + `.ready_at IS NULL AND ` + node + `.claimed_by IS NULL AND ` + q.at(spec, "approval") + ` IS NOT NULL AND EXISTS (SELECT 1 FROM ` + q.array(q.at(spec, "closures", "skip_if"), "approval_skip") + `)`
	return `((` + node + `.status='done' AND ` + node + `.outcome IN ('success','memoized') AND ` + node + `.expansion='expected') OR (` + approval + `))`
}

func (q *claimSQL) evaluationArrivalSQL() string {
	return `COALESCE(n.finished_at,(SELECT er.created_at FROM runs er WHERE er.team=e.team AND er.id=e.run_id))`
}

func approvalEvaluationReaderTx(ctx context.Context, tx *storeTx, c capability.Capability) (bool, error) {
	b := c.Binding
	if c.Unit != capability.UnitEval || b.Expansion == nil || b.Expansion.Kind != capability.UnitEval {
		return false, nil
	}
	row, err := readNodeTx(ctx, tx, Team(c.Team), controllerwire.NodeRef{RunID: runspec.RunID(b.RunID), NodeID: runspec.NodeID(b.NodeID)})
	if err != nil || !pendingApprovalPredicate(row) {
		return false, err
	}
	actor := Actor{Kind: ActorRunHost, Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: c.Prefix}, CapabilityPrefix: c.Prefix, Owner: OwnerLeaseRef{Kind: OwnerEval, EvalFence: runspec.EvalFence{RunID: row.Ref.RunID, SourceNodeID: row.Ref.NodeID, SourceGeneration: runspec.ClaimGeneration(b.Generation), Holder: b.Expansion.Holder, Generation: runspec.ClaimGeneration(b.Expansion.Generation)}}}
	err = checkApprovalEvaluationTx(ctx, tx, Team(c.Team), row, OpenApprovalNode{Ref: row.Ref, Actor: actor}.nodeChange())
	return err == nil, err
}
