package store

import (
	"context"
	"fmt"
	"slices"
	"strconv"

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
		if spec.Metered || spec.Data != nil || len(spec.ReservedPools) != 0 || !slices.Equal(spec.Grants, []capability.Grant{capability.NodesClaim, capability.TriggersClaim}) {
			return bindingRefusal()
		}
		push := spec.Binding.Push
		if push == nil {
			return bindingRefusal()
		}
		var owner int64
		var repository int64
		if _, err := fmt.Sscanf(push.RepositoryKey, "gh:%d", &repository); err != nil || repository <= 0 || push.RepositoryKey != "gh:"+strconv.FormatInt(repository, 10) {
			return bindingRefusal()
		}
		if err := tx.QueryRowContext(ctx, `SELECT repository_owner_id FROM github_runner_bindings WHERE team=? AND repository_id=?`+tx.forShare(), spec.Team, repository).Scan(&owner); err != nil {
			return err
		}
		registration := "github:" + strconv.FormatInt(repository, 10) + ":" + strconv.FormatInt(owner, 10)
		if spec.RegistrationID != registration || spec.Subject.ID != registration+":"+push.WorkflowRunID {
			return bindingRefusal()
		}
	default:
		return bindingRefusal()
	}
	return nil
}

var _ ProvisionPolicy = nativeProvisionPolicy{}
