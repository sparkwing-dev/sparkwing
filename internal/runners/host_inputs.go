package runners

import (
	"context"
	"reflect"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/engine"
	"github.com/sparkwing-dev/sparkwing/internal/nodehost"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
	"github.com/sparkwing-dev/sparkwing/pkg/apierr"
)

// HostInputs contains validated native execution input for an award.
type HostInputs struct {
	Run  nodehost.RunInput
	Node nodehost.NodeInput
}

// NativeInputs validates an award against persisted execution facts.
func NativeInputs(ctx context.Context, state nodehost.NativeHostStore, unit controllerwire.ClaimedUnit, principal capability.Principal, input nodehost.RunInput) (HostInputs, error) {
	snapshot, err := state.ReadSnapshot(ctx, unit.RunID)
	if err != nil {
		return HostInputs{}, err
	}
	if !reflect.DeepEqual(snapshot.Run.Request, unit.Request) || (snapshot.Run.Spec == nil) != (unit.Accepted == nil) {
		return HostInputs{}, awardMismatch()
	}
	if snapshot.Run.Spec != nil {
		persisted := unit
		persisted.Accepted = &controllerwire.PlanAccepted{Spec: *snapshot.Run.Spec, Plan: snapshot.Run.Plan, PlanHash: runspec.Hash(snapshot.Run.PlanHash)}
		accepted, err := nodehost.NewRunInput(persisted)
		if err != nil {
			return HostInputs{}, err
		}
		if !reflect.DeepEqual(accepted.Accepted, input.Accepted) {
			return HostInputs{}, awardMismatch()
		}
	}
	var node nodehost.NodeInput
	if unit.Unit == capability.UnitNode {
		row, err := state.ReadNode(ctx, runspec.NodeRef{RunID: runspec.RunID(unit.RunID), NodeID: runspec.NodeID(unit.JobID)})
		if err != nil {
			return HostInputs{}, err
		}
		node, err = nodehost.NewNodeInput(*input.Accepted, row, engine.Resolved{Identity: *unit.Engine}, input.GrantedRunner)
		if err != nil {
			return HostInputs{}, err
		}
		if row.Ref != (runspec.NodeRef{RunID: runspec.RunID(unit.RunID), NodeID: runspec.NodeID(unit.JobID)}) || row.CapabilityPrefix != principal.Capability.Prefix || row.ClaimedBy != unit.Binding.Holder || row.Generation != runspec.ClaimGeneration(unit.Binding.Generation) || row.Attempt != runspec.AttemptOrdinal(unit.Binding.Attempt) || !reflect.DeepEqual(row.Execution, *unit.Execution) {
			return HostInputs{}, awardMismatch()
		}
	} else if unit.Unit == capability.UnitEval {
		binding := principal.Capability.Binding
		row, err := state.ReadNode(ctx, runspec.NodeRef{RunID: runspec.RunID(unit.RunID), NodeID: runspec.NodeID(unit.JobID)})
		if err != nil {
			return HostInputs{}, err
		}
		if row.Generation != runspec.ClaimGeneration(unit.SourceGeneration) || row.Ref != (runspec.NodeRef{RunID: runspec.RunID(unit.RunID), NodeID: runspec.NodeID(unit.JobID)}) || row.Status != runstate.StatusDone || row.Outcome != runstate.NodeSuccess && row.Outcome != runstate.NodeMemoized || row.Expansion != runstate.ExpansionExpected {
			return HostInputs{}, awardMismatch()
		}
		input.Owner = runstate.Actor{Kind: runstate.ActorRunHost, Authority: principal.Authority, CapabilityPrefix: principal.Capability.Prefix, NodeFence: runspec.NodeFence{RunID: runspec.RunID(binding.RunID), NodeID: runspec.NodeID(binding.NodeID), Holder: binding.Holder, Generation: runspec.ClaimGeneration(binding.Generation)}}
	} else {
		binding := principal.Capability.Binding
		owner := snapshot.Run.Owner
		if owner == nil || owner.Kind != runstate.OwnerTrigger || owner.RunID != runspec.RunID(binding.RunID) || owner.Holder != binding.Holder || owner.Generation != runspec.ClaimGeneration(binding.Generation) {
			return HostInputs{}, awardMismatch()
		}
		input.Owner = runstate.Actor{
			Kind: runstate.ActorRunHost, Authority: principal.Authority, CapabilityPrefix: principal.Capability.Prefix,
			Owner: *owner, NodeFence: runspec.NodeFence{RunID: runspec.RunID(binding.RunID), NodeID: runspec.NodeID(binding.NodeID), Holder: binding.Holder, Generation: runspec.ClaimGeneration(binding.Generation)},
		}
	}
	return HostInputs{Run: input, Node: node}, nil
}

// ValidateAwardPrincipal checks an award against its authenticated persisted authority.
func ValidateAwardPrincipal(unit controllerwire.ClaimedUnit, principal capability.Principal) error {
	if !principalMatches(unit, principal) {
		return awardMismatch()
	}
	return nil
}

func principalMatches(unit controllerwire.ClaimedUnit, principal capability.Principal) bool {
	c := principal.Capability
	binding := c.Binding
	if unit.Unit == capability.UnitNode && unit.Binding.Kind == capability.BindingNode && unit.Binding.Expansion == nil && unit.Binding.Attempt == 0 && binding.Attempt > 0 {
		binding.Attempt = 0
	}
	if binding.ClaimBy != nil && unit.Binding.ClaimBy != nil && binding.ClaimBy.Equal(*unit.Binding.ClaimBy) {
		binding.ClaimBy = unit.Binding.ClaimBy
	}
	if binding.Deadline != nil && unit.Binding.Deadline != nil && binding.Deadline.Equal(*unit.Binding.Deadline) {
		binding.Deadline = unit.Binding.Deadline
	}
	return c.Prefix != "" && principal.Authority == (capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: c.Prefix}) && (c.Issuance == capability.Award || c.Issuance == capability.Exchange) && c.Team == unit.Request.Request.Team && c.Unit == unit.Unit && c.UnitID == unit.UnitID && c.Purpose == unit.Purpose && c.Delivery == unit.Delivery && reflect.DeepEqual(binding, unit.Binding)
}

func awardMismatch() error {
	return &apierr.Error{Code: apierr.LockHeld, Message: "runner award differs from its authenticated claim"}
}
