package store

import (
	"context"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/apierr"
)

func resolveProvisionAuthorityTx(ctx context.Context, tx *storeTx, ref capability.AuthorityRef, proof *capability.ConsoleProof) (capability.Principal, error) {
	p, err := tx.ResolveAuthority(ctx, ref)
	if err != nil || proof == nil {
		return p, err
	}
	if proof.Session != ref || tx.store.consolePolicy == nil || !tx.store.consolePolicy(p, tx.now(ctx)) {
		return capability.Principal{}, authorityError(apierr.Forbidden, "direct operator session required")
	}
	p.Class = capability.CredentialConsole
	p.Capability.Team = ""
	p.Capability.Grants = []capability.Grant{capability.OperatorConsole}
	return p, nil
}
