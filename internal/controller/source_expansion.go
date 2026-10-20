package controller

import (
	"context"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

func (s *EngineController) sourceExpansionAuthorization(ctx context.Context, a capability.Authorized, generation int64) (capability.Authorized, error) {
	if s.authority == nil {
		return a, missingEndpointPolicy()
	}
	parent, err := s.authority.ResolveAuthority(ctx, a.Principal.Authority)
	if err != nil {
		return a, err
	}
	tenant, err := s.tenantForTeam(ctx, store.Team(a.Target.Team))
	if err != nil {
		return a, err
	}
	source, err := tenant.ReadNode(ctx, controllerwire.NodeRef{RunID: a.Target.RunID, NodeID: a.Target.NodeID})
	if err != nil {
		return a, engineLookupError(err)
	}
	if source.Generation != generation {
		return a, &apierr.Error{Code: apierr.LockHeld, Message: "expansion names another source generation"}
	}
	binding := parent.Capability.Binding
	binding.Kind, binding.NodeID, binding.Generation, binding.Attempt = capability.BindingNode, source.Ref.NodeID, source.Generation, source.Attempt
	binding.Holder = ""
	binding.Expansion = &capability.LeaseRef{Kind: capability.UnitTrigger, ID: parent.Capability.UnitID, Holder: parent.Capability.Binding.Holder, Generation: parent.Capability.Binding.Generation}
	issued, err := s.authority.DelegateCapability(ctx, capability.DelegateSpec{Parent: parent.Authority, Audience: capability.Controller, Grants: []capability.Grant{capability.ExpansionSubmit}, Binding: binding, Repositories: parent.Capability.Repositories, ExpiresAt: parent.Capability.ExpiresAt})
	if err != nil {
		return a, err
	}
	a.Principal = capability.Principal{Class: capability.CredentialBearer, Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: issued.Capability.Prefix}, Capability: issued.Capability}
	a.Binding = issued.Capability.Binding
	a.Result = nil
	return a, nil
}
