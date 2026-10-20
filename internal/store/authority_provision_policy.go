package store

import (
	"context"
	"slices"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

// ProvisionPolicy verifies the bootstrap gate, enrollment or Actions evidence in the issuing transaction.
type ProvisionPolicy interface {
	AuthorizeProvision(context.Context, capability.AuthorityResolver, capability.ProvisionSpec) error
}

// SetProvisionPolicy installs trusted origin verification before concurrent store use.
func (s *Store) SetProvisionPolicy(policy ProvisionPolicy) { s.provisions = policy }

type nativeProvisionPolicy struct{}

func (nativeProvisionPolicy) AuthorizeProvision(ctx context.Context, resolver capability.AuthorityResolver, spec capability.ProvisionSpec) error {
	tx, ok := resolver.(*storeTx)
	if !ok {
		return bindingRefusal()
	}
	switch spec.Kind {
	case capability.ProvisionBootstrap:
		if tx.rotatingRoot == nil && (spec.Seed == "" || spec.Subject != (capability.Subject{Kind: capability.SubjectOperator, ID: "bootstrap:admin"}) || spec.ExpiresAt != nil || spec.Metered || !slices.Equal(spec.Grants, []capability.Grant{capability.OperatorTokens, capability.OperatorLimits, capability.OperatorObjectStore})) {
			return bindingRefusal()
		}
	case capability.ProvisionMachine:
		if spec.RegistrationID != "runner:"+spec.Team+":"+spec.Subject.ID {
			return bindingRefusal()
		}
	case capability.ProvisionService:
		if spec.RegistrationID != "service:"+spec.Subject.ID {
			return bindingRefusal()
		}
	case capability.ProvisionActions:
		return nil

	default:
		return bindingRefusal()
	}
	return nil
}

var _ ProvisionPolicy = nativeProvisionPolicy{}
