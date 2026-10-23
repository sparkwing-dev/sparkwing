package store

import (
	"context"
	"encoding/json"
	"slices"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
)

func bindingExpansionTx(ctx context.Context, tx *storeTx, c capability.Capability, target capability.Target) error {
	b := c.Binding
	if b.Kind != capability.BindingNode || b.Expansion == nil || b.RunID == "" || b.RunID != target.RunID || b.NodeID == "" || b.NodeID != target.NodeID || b.Generation != target.SourceGeneration {
		return bindingRefusal()
	}
	row, err := readNodeTx(ctx, tx, Team(c.Team), runspec.NodeRef{RunID: runspec.RunID(b.RunID), NodeID: runspec.NodeID(b.NodeID)})
	if err != nil {
		return err
	}
	if c.Unit == capability.UnitEval && row.Generation == runspec.ClaimGeneration(b.Generation) && pendingApprovalPredicate(row) {
		return nil
	}
	if row.Status != runstate.StatusDone || row.Generation != runspec.ClaimGeneration(b.Generation) || row.Outcome != runstate.NodeSuccess && row.Outcome != runstate.NodeMemoized || row.Expansion == runstate.ExpansionAbsent || row.Expansion == runstate.ExpansionNone {
		return bindingRefusal()
	}
	return nil
}

func bindingDeclaredReadTx(ctx context.Context, tx *storeTx, c capability.Capability, target capability.Target) error {
	if target.Reference != nil {
		if target.RunID != "" || target.NodeID != "" || target.SourceGeneration != 0 || target.Pipeline != "" {
			return bindingRefusal()
		}
		return bindingPipelineReferenceTx(ctx, tx, c, *target.Reference)
	}
	if target.Pipeline != "" {
		return bindingRefusal()
	}
	b := c.Binding
	if b.RunID != "" && b.RunID != target.RunID && target.RunID != "" && target.NodeID != "" {
		run, err := readRunTx(ctx, tx, Team(c.Team), target.RunID)
		if err != nil {
			return err
		}
		row, err := readNodeTx(ctx, tx, Team(c.Team), runspec.NodeRef{RunID: runspec.RunID(target.RunID), NodeID: runspec.NodeID(target.NodeID)})
		if err != nil {
			return err
		}
		if run.FinishedAt == nil || row.Status != runstate.StatusDone || row.Outcome != runstate.NodeSuccess && row.Outcome != runstate.NodeMemoized || runspec.ClaimGeneration(target.SourceGeneration) != 0 && row.Generation != runspec.ClaimGeneration(target.SourceGeneration) {
			return bindingRefusal()
		}
		return bindingPipelineReferenceTx(ctx, tx, c, capability.PipelineReferenceTarget{Pipeline: run.Request.Request.Pipeline, JobID: target.NodeID})
	}
	if b.RunID == "" || b.RunID != target.RunID || target.NodeID == "" {
		return bindingRefusal()
	}
	snapshot, err := readSnapshotTx(ctx, tx, Team(c.Team), b.RunID)
	if err != nil {
		return err
	}
	var source *runstate.NodeRow
	for i := range snapshot.Nodes {
		if snapshot.Nodes[i].Ref.NodeID == runspec.NodeID(target.NodeID) {
			source = &snapshot.Nodes[i]
		}
	}
	if source == nil || runspec.ClaimGeneration(target.SourceGeneration) != 0 && source.Generation != runspec.ClaimGeneration(target.SourceGeneration) {
		return bindingRefusal()
	}
	if b.Expansion != nil {
		if target.NodeID == b.NodeID && source.Generation == runspec.ClaimGeneration(b.Generation) {
			return nil
		}
		approval, err := approvalEvaluationReaderTx(ctx, tx, c)
		if err != nil {
			return err
		}
		if !approval {
			return bindingRefusal()
		}
	}
	for _, row := range snapshot.Nodes {
		if b.Kind != capability.BindingTrigger && (b.Kind != capability.BindingNode || row.Ref.NodeID != runspec.NodeID(b.NodeID)) {
			continue
		}
		var job nodeproto.JobSpec
		if err := json.Unmarshal(row.Spec, &job); err != nil {
			return err
		}
		var output nodeproto.JobSpec
		if err := json.Unmarshal(source.Spec, &output); err != nil {
			return err
		}
		if output.ID != target.NodeID {
			return bindingRefusal()
		}
		if capability.ReadsJobOutput(job, output) {
			return nil
		}
	}
	return bindingRefusal()
}

func bindingPipelineReferenceTx(ctx context.Context, tx *storeTx, c capability.Capability, target capability.PipelineReferenceTarget) error {
	b := c.Binding
	approval, err := approvalEvaluationReaderTx(ctx, tx, c)
	if err != nil {
		return err
	}
	node := (c.Unit == capability.UnitNode || approval) && b.Kind == capability.BindingNode && b.NodeID != ""
	owner := c.Unit == capability.UnitTrigger && b.Kind == capability.BindingTrigger || c.Unit == capability.UnitPlan && b.Kind == capability.BindingRun
	if (!node && !owner) || !slices.Contains(c.Grants, capability.RefsRead) || b.Expansion != nil && !approval || b.RunID == "" || target.Pipeline == "" {
		return bindingRefusal()
	}
	snapshot, err := readSnapshotTx(ctx, tx, Team(c.Team), b.RunID)
	if err != nil {
		return err
	}
	if snapshot.Run.CancelRequestedAt != nil || snapshot.Run.FinishedAt != nil {
		return bindingRefusal()
	}
	state, err := authenticationBindingTx(ctx, tx, c)
	if err != nil {
		return err
	}
	now := tx.now(ctx)
	if !approval && (state.FinishedAt != nil || state.RunCancelled || state.Current.Generation != b.Generation || state.Current.Holder != b.Holder || !state.LeaseExpiresAt.After(now)) {
		return bindingRefusal()
	}
	for _, row := range snapshot.Nodes {
		if node && (row.Ref.NodeID != runspec.NodeID(b.NodeID) || !approval && row.CapabilityPrefix != c.Prefix) {
			continue
		}
		var spec nodeproto.JobSpec
		if err := json.Unmarshal(row.Spec, &spec); err != nil {
			return err
		}
		for _, ref := range spec.References {
			if ref.Pipeline == target.Pipeline && ref.Job != "" && (target.JobID == "" || target.JobID == ref.Job) {
				return nil
			}
		}
	}
	return bindingRefusal()
}

func bindingSourceTx(ctx context.Context, tx *storeTx, c capability.Capability, target capability.Target) error {
	b := c.Binding
	if b.RunID == "" || b.RunID != target.RunID || !slices.Contains(c.Grants, capability.SourceRead) {
		return bindingRefusal()
	}
	run, err := readRunTx(ctx, tx, Team(c.Team), b.RunID)
	if err != nil {
		return err
	}
	if c.Data == nil || c.Data.RepositoryKey == "" || c.Data.RepositoryKey != string(run.Request.Request.Git.Repository.Key) || run.CancelRequestedAt != nil || run.FinishedAt != nil {
		return bindingRefusal()
	}
	state, err := authenticationBindingTx(ctx, tx, c)
	if err != nil {
		return err
	}
	now := tx.now(ctx)
	if b.Expansion != nil {
		lease := state.Expansion
		if lease == nil || lease.Lease != *b.Expansion || !lease.ExpiresAt.After(now) || lease.Lease.Kind == capability.UnitEval && (lease.ControllerLeaseExpiresAt == nil || !lease.ControllerLeaseExpiresAt.After(now)) {
			return bindingRefusal()
		}
		return nil
	}
	if state.RunCancelled || state.FinishedAt != nil || state.Current.Generation != b.Generation || state.Current.Holder != b.Holder || !state.LeaseExpiresAt.After(now) {
		return bindingRefusal()
	}
	if b.Kind == capability.BindingNode {
		row, err := readNodeTx(ctx, tx, Team(c.Team), runspec.NodeRef{RunID: runspec.RunID(b.RunID), NodeID: runspec.NodeID(b.NodeID)})
		if err != nil {
			return err
		}
		if row.StartedAt != nil {
			return bindingRefusal()
		}
	} else if b.Kind != capability.BindingTrigger && (b.Kind != capability.BindingRun || c.Unit != capability.UnitPlan) {
		return bindingRefusal()
	}
	return nil
}
