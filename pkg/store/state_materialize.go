package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"slices"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

type materializationRecords interface {
	CopyMaterializedRecords(context.Context, *storeTx, Team, Actor, NodeRef, MaterializeSource) (materializedRecords, error)
}

type materializedRecords struct {
	Output, Manifest *blob.RecordKey
}

func materializeChangeTx(ctx context.Context, tx *storeTx, team Team, change NodeTransition) (NodeTransition, error) {
	if change.Actor.Kind != ActorRunHost || change.Extra.Materialize == nil || change.Extra.Result != nil {
		return change, authorityError(apierr.BadRequest, "materialization requires a persisted source without a supplied result")
	}
	source := *change.Extra.Materialize
	if err := validateReconcileRequest(ReconcileRequest{Ref: change.Ref, Actor: change.Actor, Policy: source.Kind, Source: &source}); err != nil {
		return change, err
	}
	if err := checkOwnerTx(ctx, tx, team, change.Actor, change.Ref.RunID, tx.now()); err != nil {
		return change, err
	}
	if err := lockReconcileRunsTx(ctx, tx, team, change.Ref, &source); err != nil {
		return change, err
	}
	run, err := readRunTx(ctx, tx, team, change.Ref.RunID)
	if err != nil {
		return change, err
	}
	node, err := readNodeTx(ctx, tx, team, change.Ref)
	if err != nil {
		return change, err
	}
	if run.CancelRequestedAt != nil || node.CancelRequestedAt != nil || run.FinishedAt != nil {
		return change, holderRefusal()
	}
	if reason := runGuardRefusal(run); reason != "" {
		return change, authorityError(apierr.IllegalTransition, "guard refused materialization")
	}
	if node.ClaimedBy != "" {
		if err := checkSelfClaimTx(ctx, tx, team, change.Actor, node); err != nil {
			return change, err
		}
	}
	result, err := materializationResultTx(ctx, tx, team, run, node, source)
	if err != nil {
		return change, err
	}
	if change.To != "" && change.To != result.Outcome {
		return change, authorityError(apierr.IllegalTransition, "materialization outcome differs from its persisted source")
	}
	if tx.store.materializations == nil {
		return change, authorityError(apierr.Unavailable, "transactional materialization records are not configured")
	}
	records, err := tx.store.materializations.CopyMaterializedRecords(ctx, tx, team, change.Actor, change.Ref, source)
	if err != nil {
		return change, err
	}
	if err := checkOwnerTx(ctx, tx, team, change.Actor, change.Ref.RunID, tx.now()); err != nil {
		return change, err
	}
	if node.ClaimedBy != "" {
		if err := checkSelfClaimTx(ctx, tx, team, change.Actor, node); err != nil {
			return change, err
		}
	}
	result.OutputRecord, result.ManifestRecord = records.Output, records.Manifest
	change.To, change.Extra.Result = result.Outcome, &result
	return change, nil
}

func checkSelfClaimTx(ctx context.Context, tx *storeTx, team Team, actor Actor, node NodeRow) error {
	if node.Status != StatusPending || node.OwnerCapabilityPrefix != actor.CapabilityPrefix || node.ClaimedBy != actor.Owner.Holder || node.LeaseExpiresAt == nil || !node.LeaseExpiresAt.After(tx.now()) {
		return holderRefusal()
	}
	chain, err := authorityChainTx(ctx, tx, capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: node.CapabilityPrefix}, true)
	if err != nil {
		return err
	}
	owner, err := authorityChainTx(ctx, tx, actor.Authority, true)
	if err != nil {
		return err
	}
	claim := chain[0].principal.Capability
	if chain[len(chain)-1].principal.Authority != owner[len(owner)-1].principal.Authority || claim.Subject != owner[0].principal.Capability.Subject || claim.Team != string(team) || claim.Binding.RunID != node.Ref.RunID || claim.Binding.NodeID != node.Ref.NodeID || claim.Binding.Generation != node.Generation || claim.Binding.Holder != node.ClaimedBy {
		return holderRefusal()
	}
	return nil
}

func materializationResultTx(ctx context.Context, tx *storeTx, team Team, run RunRow, target NodeRow, source MaterializeSource) (NodeResult, error) {
	ref, err := materializationRef(team, source)
	if err != nil {
		return NodeResult{}, err
	}
	if source.Kind == SourceMemo {
		if source.Record.Kind != blob.KindMemo {
			return NodeResult{}, authorityError(apierr.BadRequest, "memo materialization requires a memo record")
		}
		return NodeResult{Outcome: NodeMemoized}, nil
	}
	if source.Kind == SourceResume && ref != target.Ref {
		return NodeResult{}, holderRefusal()
	}
	if source.Kind == SourceManualRetry && (ref.RunID != run.Lineage.RetryOf || ref.NodeID != target.Ref.NodeID) {
		return NodeResult{}, holderRefusal()
	}
	node, err := readNodeTx(ctx, tx, team, ref)
	if err != nil {
		return NodeResult{}, err
	}
	if node.Status != StatusDone || node.SpecHash == "" || node.SpecHash != target.SpecHash {
		return NodeResult{}, authorityError(apierr.IllegalTransition, "materialization source is not a matching terminal attempt")
	}
	if source.Result != nil {
		if node.StateSeq != source.Result.StateSeq {
			return NodeResult{}, authorityError(apierr.IllegalTransition, "materialized result fence does not match")
		}
	} else if node.Generation != source.Source.Generation || node.Attempt != source.Source.Attempt {
		return NodeResult{}, authorityError(apierr.IllegalTransition, "source attempt fence does not match")
	}
	if source.Kind == SourceManualRetry && !slices.Contains([]NodeState{NodeSuccess, NodeMemoized}, node.Outcome) {
		return NodeResult{}, authorityError(apierr.IllegalTransition, "manual retry must execute an unsuccessful source")
	}
	if source.Result == nil {
		var attemptOutcome NodeState
		var finished sql.NullInt64
		err = tx.QueryRowContext(ctx, `SELECT outcome,finished_at FROM node_attempts WHERE team=? AND run_id=? AND node_id=? AND attempt=? AND generation=?`, team, ref.RunID, ref.NodeID, source.Source.Attempt, source.Source.Generation).Scan(&attemptOutcome, &finished)
		if err != nil {
			return NodeResult{}, err
		}
		if !finished.Valid || attemptOutcome != node.Outcome {
			return NodeResult{}, authorityError(apierr.IllegalTransition, "source attempt and job outcome disagree")
		}
	}
	return readStoredNodeResultTx(ctx, tx, team, node)
}

func readStoredNodeResultTx(ctx context.Context, tx *storeTx, team Team, node NodeRow) (NodeResult, error) {
	ref := node.Ref
	result := NodeResult{Outcome: node.Outcome}
	var failure []byte
	err := tx.QueryRowContext(ctx, `SELECT failure_reason,error,exit_code,failure_json FROM nodes WHERE team=? AND run_id=? AND node_id=?`, team, ref.RunID, ref.NodeID).Scan(&result.Reason, &result.MaskedError, &result.ExitCode, &failure)
	if err == nil && len(failure) > 0 && node.Outcome == NodeFailed {
		err = json.Unmarshal(failure, &result.Failure)
		if err == nil && result.Failure == nil {
			err = authorityError(apierr.IntegrityFailure, "materialized failure is null")
		}
		if err == nil {
			err = validateRecordedFailure(ref, result.Failure)
		}
	}
	return result, err
}

func materializationRef(team Team, source MaterializeSource) (NodeRef, error) {
	if source.Kind == SourceMemo {
		if source.Result != nil || source.Record.Team != string(team) || source.Record.Kind != blob.KindMemo {
			return NodeRef{}, authorityError(apierr.BadRequest, "memo materialization requires a tenant memo record")
		}
		return NodeRef{}, nil
	}
	if source.Result != nil {
		result := source.Result
		if source.Source != (runspec.AttemptIdentity{}) || source.Kind == SourceMemo || result.Ref.RunID == "" || result.Ref.NodeID == "" || result.StateSeq <= 0 {
			return NodeRef{}, authorityError(apierr.BadRequest, "invalid materialized result identity")
		}
		return result.Ref, nil
	}
	if source.Source.Team != string(team) || source.Source.RunID == "" || source.Source.NodeID == "" || source.Source.Generation <= 0 || source.Source.Attempt <= 0 {
		return NodeRef{}, authorityError(apierr.BadRequest, "invalid materialization source identity")
	}
	return NodeRef{RunID: source.Source.RunID, NodeID: source.Source.NodeID}, nil
}
