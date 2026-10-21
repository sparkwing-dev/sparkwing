package store

import (
	"context"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

func resolveProvisionAuthorityTx(ctx context.Context, tx *storeTx, ref capability.AuthorityRef, proof *capability.ConsoleProof) (capability.Principal, error) {
	p, err := tx.ResolveAuthority(ctx, ref)
	if err != nil || proof == nil {
		return p, err
	}
	clockTime1, clockErr1 := tx.now(ctx)
	if clockErr1 != nil {
		return capability.Principal{}, clockErr1
	}
	if proof.Session != ref || tx.store.consolePolicy == nil || !tx.store.consolePolicy(p, clockTime1) {
		return capability.Principal{}, authorityError(apierr.Forbidden, "direct operator session required")
	}
	p.Class = capability.CredentialConsole
	p.Capability.Subject.Kind = capability.SubjectOperator
	p.Capability.Team = ""
	p.Capability.Grants = []capability.Grant{capability.OperatorConsole}
	return p, nil
}
