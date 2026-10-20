package store

import (
	"context"
	"slices"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
)

func bindingSourceReportTx(ctx context.Context, tx *storeTx, c capability.Capability, target capability.Target) error {
	b := c.Binding
	if c.Audience != capability.Controller || b.Kind != capability.BindingNode || b.Expansion == nil || b.RunID == "" || b.RunID != target.RunID || b.NodeID == "" || target.NodeID != "" && target.NodeID != b.NodeID || b.Generation < 0 {
		return bindingRefusal()
	}
	request := ExpansionSubmission{Source: controllerwire.NodeRef{RunID: b.RunID, NodeID: b.NodeID}, SourceGeneration: b.Generation, Actor: Actor{Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: c.Prefix}, CapabilityPrefix: c.Prefix}}
	if err := expansionAuthorityTx(ctx, tx, Team(c.Team), request, tx.now()); err != nil {
		return err
	}
	if err := lockStateRunTx(ctx, tx, Team(c.Team), b.RunID); err != nil {
		return err
	}
	run, err := readRunTx(ctx, tx, Team(c.Team), b.RunID)
	if err != nil {
		return err
	}
	row, err := readNodeTx(ctx, tx, Team(c.Team), request.Source)
	if err != nil {
		return err
	}
	if run.CancelRequestedAt != nil || run.FinishedAt != nil || row.Generation != b.Generation || row.Status != StatusDone || row.Outcome != NodeSuccess && row.Outcome != NodeMemoized {
		return bindingRefusal()
	}
	return nil
}

func checkMetadataWriterTx(ctx context.Context, tx *storeTx, team Team, ref controllerwire.NodeRef, actor Actor, step string) (NodeRow, error) {
	if step != "" {
		return checkResultWriterTx(ctx, tx, team, ref, actor)
	}
	p, err := resolveActorTx(ctx, tx, actor)
	if err != nil {
		return NodeRow{}, err
	}
	c := p.Capability
	if c.Team != string(team) || c.Audience != capability.Controller || c.Prefix != actor.CapabilityPrefix || c.Binding.RunID != ref.RunID {
		return NodeRow{}, holderRefusal()
	}
	if c.Binding.Expansion != nil {
		err = bindingSourceReportTx(ctx, tx, c, capability.Target{Team: string(team), RunID: ref.RunID, NodeID: ref.NodeID})
	} else if c.Unit == capability.UnitTrigger || c.Unit == capability.UnitPlan {
		if !slices.Contains(c.Grants, capability.NodeReport) || actor.Kind != ActorRunHost {
			return NodeRow{}, holderRefusal()
		}
		err = checkOwnerLeaseTx(ctx, tx, team, actor, ref.RunID, tx.now())
		if err == nil {
			err = lockStateRunTx(ctx, tx, team, ref.RunID)
		}
		if err == nil {
			run, readErr := readRunTx(ctx, tx, team, ref.RunID)
			if readErr != nil {
				return NodeRow{}, readErr
			}
			if run.FinishedAt != nil {
				return NodeRow{}, holderRefusal()
			}
		}
	} else {
		return checkResultWriterTx(ctx, tx, team, ref, actor)
	}
	if err != nil {
		return NodeRow{}, err
	}
	return readNodeTx(ctx, tx, team, ref)
}
