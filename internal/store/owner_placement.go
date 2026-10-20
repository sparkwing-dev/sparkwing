package store

import (
	"context"
	"encoding/json"
	"errors"
	"math"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/pkg/match"
)

// OwnerExecution binds a private placement decision to its owner and declaration.
type OwnerExecution struct {
	Owner          capability.AuthorityRef
	Team           Team
	Generation     int64
	Node           controllerwire.NodeRef
	NodeGeneration int64
	SpecHash       string
	Allowed        bool
}

// OwnerCanExecute checks private placement decisions against the authenticated owner.
func OwnerCanExecute(owner capability.Principal) func(NodeRow) bool {
	return func(node NodeRow) bool {
		p, c := node.OwnerExecution, owner.Capability
		return p != nil && p.Allowed && p.Owner == owner.Authority && p.Team == Team(c.Team) &&
			owner.Authority == (capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: c.Prefix}) &&
			c.Binding.Kind == capability.BindingTrigger && p.Generation > 0 && p.Generation == c.Binding.Generation &&
			p.Node.RunID == c.Binding.RunID && p.Node == node.Ref && p.NodeGeneration == node.Generation &&
			p.SpecHash != "" && p.SpecHash == node.SpecHash
	}
}

func ownerClaimDemandTx(ctx context.Context, tx *storeTx, owner capability.Principal) (UnitDemand, error) {
	here, err := hereObserverTx(ctx, tx, owner)
	if err != nil {
		return UnitDemand{}, err
	}
	if tx.store.claimProfiles != nil && !here {
		profile, available, err := tx.store.claimProfiles.ResolveClaimProfile(ctx, owner)
		return UnitDemand{CapabilityPrefix: owner.Capability.Prefix, Labels: profile.Labels, OS: profile.OS, Arch: profile.Arch, Capacity: profile.Capacity, Available: available}, err
	}
	chain, err := authorityChainTx(ctx, tx, owner.Authority, false)
	if err != nil {
		return UnitDemand{}, err
	}
	for _, ancestor := range chain {
		c := ancestor.principal.Capability
		if (!registeredClaimant(c) && !(here && c.Binding.RunID == owner.Capability.Binding.RunID)) || c.Subject != owner.Capability.Subject {
			continue
		}
		var body []byte
		if err := tx.QueryRowContext(ctx, `SELECT claim_observation_json FROM capabilities WHERE team=? AND prefix=?`, ancestor.principal.Capability.Team, ancestor.principal.Capability.Prefix).Scan(&body); err != nil {
			return UnitDemand{}, err
		}
		if len(body) == 0 {
			continue
		}
		var poll runnerPoll
		if err := json.Unmarshal(body, &poll); err != nil {
			return UnitDemand{}, err
		}
		if poll.ObservedAt.IsZero() || poll.ObservedAt.After(tx.now()) {
			continue
		}
		if !validMatchingResources(poll.Capacity) || !validMatchingResources(poll.Available) {
			return UnitDemand{}, authorityError(apierr.IntegrityFailure, "invalid owner capacity observation")
		}
		poll.CapabilityPrefix = owner.Capability.Prefix
		return poll.UnitDemand, nil
	}
	return UnitDemand{}, authorityError(apierr.Unavailable, "self-claim host observation is unavailable")
}

func reservedOwnerDemandTx(ctx context.Context, tx *storeTx, owner capability.Principal, reserved *match.Resources) (UnitDemand, error) {
	demand, err := ownerClaimDemandTx(ctx, tx, owner)
	if err != nil || reserved == nil {
		return demand, err
	}
	if !validMatchingResources(*reserved) || reserved.Cores > demand.Capacity.Cores || reserved.MemoryBytes > demand.Capacity.MemoryBytes || reserved.MemoryBytes > math.MaxInt64-demand.Available.MemoryBytes {
		return UnitDemand{}, authorityError(apierr.BadRequest, "invalid owner machine reservation")
	}
	demand.Available.Cores += reserved.Cores
	demand.Available.MemoryBytes += reserved.MemoryBytes
	return demand, nil
}

func ownerExecutionEligibleTx(ctx context.Context, tx *storeTx, owner capability.Principal, ref controllerwire.NodeRef, reserved *match.Resources) (bool, error) {
	demand, ok, err := ownerExecutionDemandTx(ctx, tx, owner, reserved)
	if !ok || err != nil {
		return false, err
	}
	return ownerDemandEligibleTx(ctx, tx, owner, demand, ref)
}

// safety: ok=false means the owner has no current host observation, which is not an error.
func ownerExecutionDemandTx(ctx context.Context, tx *storeTx, owner capability.Principal, reserved *match.Resources) (UnitDemand, bool, error) {
	demand, err := reservedOwnerDemandTx(ctx, tx, owner, reserved)
	if err != nil {
		var denied *apierr.Error
		if errors.As(err, &denied) && denied.Code == apierr.Unavailable {
			return UnitDemand{}, false, nil
		}
		return UnitDemand{}, false, err
	}
	return demand, true, nil
}

func ownerDemandEligibleTx(ctx context.Context, tx *storeTx, owner capability.Principal, demand UnitDemand, ref controllerwire.NodeRef) (bool, error) {
	unit := UnitRef{Kind: capability.UnitNode, RunID: ref.RunID, NodeID: ref.NodeID}
	if _, err := claimKinds(owner.Capability, UnitDemand{Target: &unit}); err != nil {
		return false, nil
	}
	profile, required, err := readMatchingInputsTx(ctx, tx, demand, unit, false)
	if err != nil {
		var denied *apierr.Error
		if errors.As(err, &denied) && denied.Code == apierr.Forbidden {
			return false, nil
		}
		return false, err
	}
	if !match.Evaluate(profile, required).OK() || !match.Fits(profile.Capacity, demand.Available, required.Request).OK() {
		return false, nil
	}
	placement, err := unitPlacementTx(ctx, tx, Team(owner.Capability.Team), unit, demand.CapabilityPrefix, profile, required)
	return !placement.hold, err
}
