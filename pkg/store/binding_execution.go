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
		if job.OnFailureOf == target.NodeID || slices.Contains(job.Needs, target.NodeID) || slices.Contains(job.NeedsIfPresent, target.NodeID) {
			return nil
		}
		for _, group := range job.NeedsGenerated {
			var generated nodeproto.JobSpec
			if err := json.Unmarshal(source.Spec, &generated); err != nil {
				return err
			}
			if slices.Contains(generated.Groups, group) {
				return nil
			}
		}
	}
	return bindingRefusal()
}

func bindingPipelineReferenceTx(ctx context.Context, tx *storeTx, c capability.Capability, target capability.Target) error {
	b := c.Binding
	if c.Unit != capability.UnitNode || b.Kind != capability.BindingNode || b.Expansion != nil || b.RunID == "" || b.NodeID == "" || target.RunID != "" || target.NodeID != "" || target.SourceGeneration != 0 {
		return bindingRefusal()
	}
	run, err := readRunTx(ctx, tx, Team(c.Team), b.RunID)
	if err != nil {
		return err
	}
	row, err := readNodeTx(ctx, tx, Team(c.Team), NodeRef{b.RunID, b.NodeID})
	if err != nil {
		return err
	}
	if run.CancelRequestedAt != nil || run.FinishedAt != nil || row.CancelRequestedAt != nil || row.FinishedAt != nil || row.Generation != b.Generation || row.ClaimedBy != b.Holder || row.CapabilityPrefix != c.Prefix || row.LeaseExpiresAt == nil || !row.LeaseExpiresAt.After(tx.now()) {
		return bindingRefusal()
	}
	var spec nodeproto.JobSpec
	if err := json.Unmarshal(row.Spec, &spec); err != nil {
		return err
	}
	for _, ref := range spec.References {
		if ref.Pipeline == target.Pipeline && ref.Job != "" {
			return nil
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
