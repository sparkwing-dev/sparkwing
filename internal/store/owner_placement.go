package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
)

// OwnerCanExecute checks private placement decisions against the authenticated owner.
func OwnerCanExecute(owner capability.Principal) func(runstate.NodeRow) bool {
	return func(node runstate.NodeRow) bool {
		p, c := node.OwnerExecution, owner.Capability
		return p != nil && p.Allowed && p.Owner == owner.Authority && p.Team == c.Team &&
			owner.Authority == (capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: c.Prefix}) &&
			c.Binding.Kind == capability.BindingTrigger && p.Generation > 0 && p.Generation == c.Binding.Generation &&
			p.Node.RunID == runspec.RunID(c.Binding.RunID) && p.Node == node.Ref && runspec.ClaimGeneration(p.NodeGeneration) == node.Generation &&
			p.SpecHash != "" && p.SpecHash == node.SpecHash
	}
}

func ownerClaimDemandTx(ctx context.Context, tx *storeTx, owner capability.Principal) (UnitDemand, error) {
	here, err := hereObserverTx(ctx, tx, owner)
	if err != nil {
		return UnitDemand{}, err
	}
	if tx.store.claimProfiles != nil && !here {
		profile, err := tx.store.claimProfiles.ResolveClaimProfile(ctx, owner)
		return UnitDemand{CapabilityPrefix: owner.Capability.Prefix, Labels: profile.Labels, OS: profile.OS, Arch: profile.Arch}, err
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
		if poll.ObservedAt.IsZero() || poll.ObservedAt.After(tx.now(ctx)) {
			continue
		}
		// The owner occupies its queue slot already; only the host profile carries into self-execution.
		return UnitDemand{CapabilityPrefix: owner.Capability.Prefix, Labels: poll.Labels, OS: poll.OS, Arch: poll.Arch}, nil
	}
	return UnitDemand{}, authorityError(apierr.Unavailable, "self-claim host observation is unavailable")
}

func ownerExecutionEligibleTx(ctx context.Context, tx *storeTx, owner capability.Principal, ref runspec.NodeRef) (bool, error) {
	demand, ok, err := ownerExecutionDemandTx(ctx, tx, owner)
	if !ok || err != nil {
		return false, err
	}
	return ownerDemandEligibleTx(ctx, tx, owner, demand, ref)
}

// safety: ok=false means the owner has no current host observation, which is not an error.
func ownerExecutionDemandTx(ctx context.Context, tx *storeTx, owner capability.Principal) (UnitDemand, bool, error) {
	demand, err := ownerClaimDemandTx(ctx, tx, owner)
	if err != nil {
		var denied *apierr.Error
		if errors.As(err, &denied) && denied.Code == apierr.Unavailable {
			return UnitDemand{}, false, nil
		}
		return UnitDemand{}, false, err
	}
	return demand, true, nil
}

func ownerDemandEligibleTx(ctx context.Context, tx *storeTx, owner capability.Principal, demand UnitDemand, ref runspec.NodeRef) (bool, error) {
	unit := UnitRef{Kind: capability.UnitNode, RunID: ref.RunID, NodeID: ref.NodeID}
	if _, err := claimKinds(owner.Capability, UnitDemand{Target: &unit}); err != nil {
		return false, nil
	}
	demand.Target, demand.Kinds = &unit, nil
	q := claimSQL{projected: true}
	query, err := claimQueryTx(ctx, tx, demand, []capability.UnitKind{unit.Kind}, &q)
	if err != nil {
		return false, err
	}
	var team Team
	var selected UnitRef
	err = tx.QueryRowContext(ctx, query, q.args...).Scan(&team, &selected.RunID, &selected.NodeID, &selected.SourceGeneration, &selected.Kind)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

func ownerEligibleNodesTx(ctx context.Context, tx *storeTx, owner capability.Principal, demand UnitDemand) (map[runspec.NodeRef]bool, error) {
	unit := UnitRef{Kind: capability.UnitNode, RunID: runspec.RunID(owner.Capability.Binding.RunID)}
	if _, err := claimKinds(owner.Capability, UnitDemand{Target: &unit}); err != nil {
		return nil, nil
	}
	demand.Target, demand.Kinds = nil, nil
	q := claimSQL{projected: true}
	query, err := claimQueryTx(ctx, tx, demand, []capability.UnitKind{capability.UnitNode}, &q)
	if err != nil {
		return nil, err
	}
	rows, err := tx.queryContext(ctx, query, q.args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	eligible := make(map[runspec.NodeRef]bool)
	for rows.Next() {
		var team Team
		var selected UnitRef
		if err := rows.Scan(&team, &selected.RunID, &selected.NodeID, &selected.SourceGeneration, &selected.Kind); err != nil {
			return nil, err
		}
		eligible[runspec.NodeRef{RunID: selected.RunID, NodeID: selected.NodeID}] = true
	}
	return eligible, errors.Join(rows.Err(), rows.Close())
}
