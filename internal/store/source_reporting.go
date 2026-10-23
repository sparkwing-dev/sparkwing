package store

import (
	"context"
	"slices"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
)

func bindingSourceReportTx(ctx context.Context, tx *storeTx, c capability.Capability, target capability.Target) error {
	b := c.Binding
	if c.Audience != capability.Controller || b.Kind != capability.BindingNode || b.Expansion == nil || b.RunID == "" || b.RunID != target.RunID || b.NodeID == "" || target.NodeID != "" && target.NodeID != b.NodeID || b.Generation < 0 {
		return bindingRefusal()
	}
	request := ExpansionSubmission{Source: runspec.NodeRef{RunID: runspec.RunID(b.RunID), NodeID: runspec.NodeID(b.NodeID)}, SourceGeneration: runspec.ClaimGeneration(b.Generation), Actor: runstate.Actor{Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: c.Prefix}, CapabilityPrefix: c.Prefix}}
	if b.Expansion.Kind == capability.UnitEval {
		request.Actor.Owner = runstate.OwnerLeaseRef{Kind: runstate.OwnerEval, EvalFence: runspec.EvalFence{
			RunID: runspec.RunID(b.RunID), SourceNodeID: runspec.NodeID(b.NodeID), SourceGeneration: runspec.ClaimGeneration(b.Generation),
			Holder: b.Expansion.Holder, Generation: runspec.ClaimGeneration(b.Expansion.Generation),
		}}
	}
	if err := expansionAuthorityTx(ctx, tx, Team(c.Team), request); err != nil {
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
	if run.CancelRequestedAt != nil || run.FinishedAt != nil || row.Generation != runspec.ClaimGeneration(b.Generation) || row.Status != runstate.StatusDone || row.Outcome != runstate.NodeSuccess && row.Outcome != runstate.NodeMemoized {
		return bindingRefusal()
	}
	return expansionAuthorityTx(ctx, tx, Team(c.Team), request)
}

func checkMetadataWriterTx(ctx context.Context, tx *storeTx, team Team, ref runspec.NodeRef, actor runstate.Actor, step string) (runstate.NodeRow, error) {
	if step != "" {
		return checkResultWriterTx(ctx, tx, team, ref, actor)
	}
	p, err := resolveActorTx(ctx, tx, actor)
	if err != nil {
		return runstate.NodeRow{}, err
	}
	c := p.Capability
	if c.Team != string(team) || c.Audience != capability.Controller || c.Prefix != actor.CapabilityPrefix || runspec.RunID(c.Binding.RunID) != ref.RunID {
		return runstate.NodeRow{}, holderRefusal()
	}
	if c.Binding.Expansion != nil {
		err = bindingSourceReportTx(ctx, tx, c, capability.Target{Team: string(team), RunID: string(ref.RunID), NodeID: string(ref.NodeID)})
	} else if c.Unit == capability.UnitTrigger || c.Unit == capability.UnitPlan {
		if !slices.Contains(c.Grants, capability.NodeReport) || actor.Kind != runstate.ActorRunHost {
			return runstate.NodeRow{}, holderRefusal()
		}

		err = checkOwnerLeaseTx(ctx, tx, team, actor, string(ref.RunID))
		if err == nil {
			err = lockStateRunTx(ctx, tx, team, string(ref.RunID))
		}
		if err == nil {
			run, readErr := readRunTx(ctx, tx, team, string(ref.RunID))
			if readErr != nil {
				return runstate.NodeRow{}, readErr
			}
			if run.FinishedAt != nil {
				return runstate.NodeRow{}, holderRefusal()
			}
		}
	} else {
		return checkResultWriterTx(ctx, tx, team, ref, actor)
	}
	if err != nil {
		return runstate.NodeRow{}, err
	}
	return readNodeTx(ctx, tx, team, ref)
}
