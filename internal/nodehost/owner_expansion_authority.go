package nodehost

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// OwnerDelegator resolves current owner ceilings and persists source expansion authority.
type OwnerDelegator interface {
	capability.AuthorityResolver
	DelegateCapability(context.Context, capability.DelegateSpec) (capability.Issued, error)
}

func (h *OwnerHost) expansionActor(ctx context.Context, input RunInput, row store.NodeRow) (store.Actor, error) {
	if h.ExpansionActor != nil {
		return h.ExpansionActor(ctx, input, row)
	}
	owner := input.Owner
	if h.Delegator == nil || owner.Kind != store.ActorRunHost || owner.Owner.Kind != store.OwnerTrigger || owner.Owner.RunID != input.RunID || row.Ref.RunID != input.RunID || row.Ref.NodeID == "" || row.Generation < 0 {
		return store.Actor{}, errors.New("expansion requires source-bound owner delegation")
	}
	parent, err := h.Delegator.ResolveAuthority(ctx, owner.Authority)
	if err != nil {
		return store.Actor{}, err
	}
	binding := parent.Capability.Binding
	if parent.Authority != owner.Authority || binding.Kind != capability.BindingTrigger || binding.RunID != input.RunID || binding.Holder != owner.Owner.Holder || binding.Generation != owner.Owner.Generation {
		return store.Actor{}, errors.New("expansion parent differs from its current owner")
	}
	binding.Kind, binding.NodeID, binding.Generation, binding.Attempt = capability.BindingNode, row.Ref.NodeID, row.Generation, row.Attempt
	binding.Holder = ""
	binding.Expansion = &capability.LeaseRef{Kind: capability.UnitTrigger, ID: input.RunID, Holder: owner.Owner.Holder, Generation: owner.Owner.Generation}
	expected, err := json.Marshal(binding)
	if err != nil {
		return store.Actor{}, err
	}
	issued, err := h.Delegator.DelegateCapability(ctx, capability.DelegateSpec{Parent: owner.Authority, Audience: capability.Controller, Grants: []capability.Grant{capability.ExpansionSubmit}, Binding: binding, Repositories: parent.Capability.Repositories, ExpiresAt: parent.Capability.ExpiresAt})
	if err != nil {
		return store.Actor{}, err
	}
	credential := issued.Capability
	actual, err := json.Marshal(credential.Binding)
	if err != nil {
		return store.Actor{}, err
	}
	if credential.Prefix == "" || credential.Parent == nil || *credential.Parent != owner.Authority || credential.Audience != capability.Controller || !bytes.Equal(actual, expected) || !reflect.DeepEqual(credential.Grants, []capability.Grant{capability.ExpansionSubmit}) {
		return store.Actor{}, errors.New("expansion delegation changed its source or authority")
	}
	return store.Actor{Kind: store.ActorRunHost, Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: credential.Prefix}, CapabilityPrefix: credential.Prefix, Owner: owner.Owner}, nil
}

var _ OwnerDelegator = (*store.Store)(nil)
