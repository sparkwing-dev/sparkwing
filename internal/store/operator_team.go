package store

import (
	"context"
	"slices"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/apierr"
)

// CreateOperatorTeam commits a new team, its first administrator and the operator audit together.
func (o *Operator) CreateOperatorTeam(ctx context.Context, a capability.Authorized, slug, displayName string) (capability.Issued, error) {
	var issued capability.Issued
	err := o.s.inTx(ctx, func(tx *storeTx) error {
		p, err := resolveProvisionAuthorityTx(ctx, tx, a.Principal.Authority, capability.ProofForConsole(a.Principal))
		if err != nil {
			return err
		}
		if !(capability.DeploymentAuthority(p) && slices.Contains(p.Capability.Grants, capability.OperatorTokens) || p.Class == capability.CredentialConsole && slices.Contains(p.Capability.Grants, capability.OperatorConsole)) {
			return bindingRefusal()
		}
		if err := ValidateSlug(slug); err != nil {
			return authorityError(apierr.BadRequest, "invalid team slug")
		}
		displayName = strings.TrimSpace(displayName)
		if displayName == "" {
			displayName = slug
		}
		if len(displayName) > maxDisplayNameLen {
			return authorityError(apierr.BadRequest, "team display name is too long")
		}
		account, err := newIdentityID()
		if err != nil {
			return err
		}
		now := tx.now(ctx).UnixNano()
		if _, err := tx.ExecContext(ctx, `INSERT INTO accounts(id,email,name,active_team,created_at,updated_at) VALUES (?,'',?,?,?,?)`, account, displayName, slug, now, now); err != nil {
			return err
		}
		if err := createTeamTx(ctx, tx, account, Team(slug), displayName, auditPrincipalActor(p), tx.now(ctx)); err != nil {
			return err
		}
		tx.createdTeam = &TeamInfo{Slug: Team(slug), CreatedBy: account}
		grants := []capability.Grant{}
		for _, scope := range ScopesForRole(RoleOwner) {
			grants = append(grants, capability.Grant(scope))
		}
		issued, err = o.s.Issue(ctx, tx, capability.IssueRequest{Kind: capability.Delegate, Provision: &capability.ProvisionSpec{
			Kind: capability.ProvisionTeamAdmin, Authority: &p.Authority, ConsoleProof: capability.ProofForConsole(p),
			Team: slug, Subject: capability.Subject{Kind: capability.SubjectAccount, ID: account},
			Grants: grants, Binding: capability.Binding{Kind: capability.BindingNone},
		}})
		return err
	})
	if err != nil {
		return capability.Issued{}, err
	}
	return issued, nil
}
