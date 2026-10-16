package store

import (
	"context"
	"slices"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

type delegationContracts interface {
	ValidateDelegation(capability.Principal, capability.DelegateSpec, time.Time) error
}

func delegateCapabilityTx(ctx context.Context, tx *storeTx, spec capability.DelegateSpec) (capability.Capability, error) {
	parent, err := tx.ResolveAuthority(ctx, spec.Parent)
	if err != nil {
		return capability.Capability{}, err
	}
	if err := capability.ValidateDelegation(parent, spec, tx.now()); err != nil {
		return capability.Capability{}, err
	}
	if tx.store.delegation != nil {
		if err := tx.store.delegation.ValidateDelegation(parent, spec, tx.now()); err != nil {
			return capability.Capability{}, err
		}
	}
	parent, err = tx.ResolveAuthority(ctx, spec.Parent)
	if err != nil {
		return capability.Capability{}, err
	}
	return capability.Capability{
		Team: parent.Capability.Team, Subject: parent.Capability.Subject,
		Audience: spec.Audience, Grants: slices.Clone(spec.Grants), Binding: spec.Binding,
		Issuance: capability.Delegate, Parent: &spec.Parent, ExpiresAt: spec.ExpiresAt,
		ReservedPools: slices.Clone(parent.Capability.ReservedPools), Repositories: slices.Clone(spec.Repositories),
		Data: spec.Data, RootMetered: parent.Capability.RootMetered,
		Unit: parent.Capability.Unit, UnitID: parent.Capability.UnitID,
		Purpose: parent.Capability.Purpose, Delivery: parent.Capability.Delivery,
		Billing: parent.Capability.Billing,
	}, nil
}

// DelegateCapability returns the narrowed bearer only after its transaction commits.
func (s *Store) DelegateCapability(ctx context.Context, spec capability.DelegateSpec) (capability.Issued, error) {
	return s.commitIssuance(ctx, capability.IssueRequest{Kind: capability.Delegate, Delegate: &spec})
}

// ProvisionCapability accepts trusted root-origin facts and rechecks its authorizer.
// It does not accept ordinary delegation input or omit origin verification.
func (s *Store) ProvisionCapability(ctx context.Context, spec capability.ProvisionSpec) (capability.Issued, error) {
	return s.commitIssuance(ctx, capability.IssueRequest{Kind: capability.Delegate, Provision: &spec})
}

func (s *Store) commitIssuance(ctx context.Context, request capability.IssueRequest) (capability.Issued, error) {
	var issued capability.Issued
	err := s.inTx(ctx, func(tx *storeTx) error {
		var err error
		issued, err = s.Issue(ctx, tx, request)
		return err
	})
	if err != nil {
		return capability.Issued{}, err
	}
	return issued, nil
}
