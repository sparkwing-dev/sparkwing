package store

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/pkg/match"
)

// OwnerExecution binds a private placement decision to its owner and declaration.
type OwnerExecution struct {
	Owner          capability.AuthorityRef
	Team           Team
	Generation     int64
	Node           NodeRef
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
	if tx.store.claimProfiles != nil {
		profile, available, err := tx.store.claimProfiles.ResolveClaimProfile(ctx, owner)
		return UnitDemand{CapabilityPrefix: owner.Capability.Prefix, Labels: profile.Labels, OS: profile.OS, Arch: profile.Arch, Capacity: profile.Capacity, Available: available}, err
	}
	chain, err := authorityChainTx(ctx, tx, owner.Authority, false)
	if err != nil {
		return UnitDemand{}, err
	}
	for _, ancestor := range chain {
		if !registeredClaimant(ancestor.principal.Capability) || ancestor.principal.Capability.Subject != owner.Capability.Subject {
			continue
		}
		var body []byte
		if err := tx.QueryRowContext(ctx, `SELECT claim_observation_json FROM capabilities WHERE prefix=?`, ancestor.principal.Capability.Prefix).Scan(&body); err != nil {
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

func ownerExecutionEligibleTx(ctx context.Context, tx *storeTx, owner capability.Principal, ref NodeRef) (bool, error) {
	demand, err := ownerClaimDemandTx(ctx, tx, owner)
	if err != nil {
		var denied *apierr.Error
		if errors.As(err, &denied) && denied.Code == apierr.Unavailable {
			return false, nil
		}
		return false, err
	}
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
	placement, err := unitPlacementTx(ctx, tx, Team(owner.Capability.Team), unit, profile, required)
	return !placement.hold, err
}
