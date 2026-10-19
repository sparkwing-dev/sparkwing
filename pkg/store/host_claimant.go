package store

import (
	"context"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

func hostClaimant(c capability.Capability) bool { return capability.HostClaimant(c) }

// IssueHostClaimant mints the self-hosted controller host's claim root. It
// claims plan, evaluation and job units of every team, unmetered, at the
// self-hosted tier. Only a deployment without a launcher may call it.
func (s *Store) IssueHostClaimant(ctx context.Context) (capability.Issued, error) {
	var issued capability.Issued
	err := s.inTx(ctx, func(tx *storeTx) error {
		c := capability.Capability{
			Subject: capability.Subject{Kind: capability.SubjectService, ID: capability.HostSubject}, Audience: capability.Controller, Issuance: capability.Delegate,
			Grants: []capability.Grant{capability.NodesClaim, capability.TriggersClaim}, Binding: capability.Binding{Kind: capability.BindingNone},
		}
		var err error
		issued, err = persistIssuedCapabilityTx(ctx, tx, c, "", &capability.ProvisionSpec{Kind: capability.ProvisionService, RegistrationID: "service:" + capability.HostSubject})
		return err
	})
	return issued, err
}

// RevokeHostClaimant revokes a root IssueHostClaimant minted.
func (s *Store) RevokeHostClaimant(ctx context.Context, prefix string) error {
	return s.inTx(ctx, func(tx *storeTx) error {
		ref := capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: prefix}
		node, err := readAuthorityTx(ctx, tx, ref, true)
		if err != nil {
			return err
		}
		if !hostClaimant(node.principal.Capability) {
			return bindingRefusal()
		}
		return revokeAuthorityTx(ctx, tx, ref)
	})
}
