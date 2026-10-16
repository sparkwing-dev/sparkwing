package store

import (
	"context"
	"encoding/json"
	"slices"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
)

func bindingExpansionTx(ctx context.Context, tx *storeTx, c capability.Capability, target capability.Target) error {
	b := c.Binding
	if b.Kind != capability.BindingNode || b.Expansion == nil || b.RunID == "" || b.RunID != target.RunID || b.NodeID == "" || b.NodeID != target.NodeID || b.Generation != target.SourceGeneration {
		return bindingRefusal()
	}
	row, err := readNodeTx(ctx, tx, Team(c.Team), NodeRef{RunID: b.RunID, NodeID: b.NodeID})
	if err != nil {
		return err
	}
	if row.Status != StatusDone || row.Generation != b.Generation || row.Outcome != NodeSuccess && row.Outcome != NodeMemoized || row.Expansion == ExpansionAbsent || row.Expansion == ExpansionNone {
		return bindingRefusal()
	}
	return nil
}

func bindingDeclaredReadTx(ctx context.Context, tx *storeTx, c capability.Capability, target capability.Target) error {
	if target.Pipeline != "" {
		return bindingPipelineReferenceTx(ctx, tx, c, target)
	}
	b := c.Binding
	if b.RunID != "" && b.RunID != target.RunID && target.RunID != "" && target.NodeID != "" {
		run, err := readRunTx(ctx, tx, Team(c.Team), target.RunID)
		if err != nil {
			return err
		}
		row, err := readNodeTx(ctx, tx, Team(c.Team), NodeRef{target.RunID, target.NodeID})
		if err != nil {
			return err
		}
		if run.FinishedAt == nil || row.Status != StatusDone || row.Outcome != NodeSuccess && row.Outcome != NodeMemoized || target.SourceGeneration != 0 && row.Generation != target.SourceGeneration {
			return bindingRefusal()
		}
		return bindingPipelineReferenceTx(ctx, tx, c, capability.Target{Pipeline: run.Request.Request.Pipeline, NodeID: target.NodeID})
	}
	if b.RunID == "" || b.RunID != target.RunID || target.NodeID == "" {
		return bindingRefusal()
	}
	snapshot, err := readSnapshotTx(ctx, tx, Team(c.Team), b.RunID)
	if err != nil {
		return err
	}
	var source *NodeRow
	for i := range snapshot.Nodes {
		if snapshot.Nodes[i].Ref.NodeID == target.NodeID {
			source = &snapshot.Nodes[i]
		}
	}
	if source == nil || target.SourceGeneration != 0 && source.Generation != target.SourceGeneration {
		return bindingRefusal()
	}
	if b.Expansion != nil {
		if target.NodeID == b.NodeID && source.Generation == b.Generation {
			return nil
		}
		return bindingRefusal()
	}
	for _, row := range snapshot.Nodes {
		if b.Kind != capability.BindingTrigger && (b.Kind != capability.BindingNode || row.Ref.NodeID != b.NodeID) {
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

func bindingPipelineReferenceTx(ctx context.Context, tx *storeTx, c capability.Capability, target capability.Target) error {
	b := c.Binding
	node := c.Unit == capability.UnitNode && b.Kind == capability.BindingNode && b.NodeID != ""
	owner := c.Unit == capability.UnitTrigger && b.Kind == capability.BindingTrigger || c.Unit == capability.UnitPlan && b.Kind == capability.BindingRun
	if (!node && !owner) || !slices.Contains(c.Grants, capability.RefsRead) || b.Expansion != nil || b.RunID == "" || target.RunID != "" || target.SourceGeneration != 0 || target.Pipeline == "" {
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
	if state.FinishedAt != nil || state.RunCancelled || state.Current.Generation != b.Generation || state.Current.Holder != b.Holder || !state.LeaseExpiresAt.After(tx.now()) {
		return bindingRefusal()
	}
	for _, row := range snapshot.Nodes {
		if node && (row.Ref.NodeID != b.NodeID || row.CapabilityPrefix != c.Prefix) {
			continue
		}
		var spec nodeproto.JobSpec
		if err := json.Unmarshal(row.Spec, &spec); err != nil {
			return err
		}
		for _, ref := range spec.References {
			if ref.Pipeline == target.Pipeline && ref.Job != "" && (target.NodeID == "" || target.NodeID == ref.Job) {
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
	if b.Expansion != nil {
		lease := state.Expansion
		if lease == nil || lease.Lease != *b.Expansion || !lease.ExpiresAt.After(tx.now()) || lease.Lease.Kind == capability.UnitEval && (lease.ControllerLeaseExpiresAt == nil || !lease.ControllerLeaseExpiresAt.After(tx.now())) {
			return bindingRefusal()
		}
		return nil
	}
	if state.RunCancelled || state.FinishedAt != nil || state.Current.Generation != b.Generation || state.Current.Holder != b.Holder || !state.LeaseExpiresAt.After(tx.now()) {
		return bindingRefusal()
	}
	if b.Kind == capability.BindingNode {
		row, err := readNodeTx(ctx, tx, Team(c.Team), NodeRef{RunID: b.RunID, NodeID: b.NodeID})
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
