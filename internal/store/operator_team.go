package store

import (
	"context"
	"database/sql"
	"errors"
	"slices"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/sparkwing/protocol/apierr"
)

// OperatorTeamAdminReissue is a fresh team administrator credential and the earlier credentials it revoked.
type OperatorTeamAdminReissue struct {
	Issued  capability.Issued
	Revoked []string
}

func operatorTeamAuthority(p capability.Principal) bool {
	return capability.DeploymentAuthority(p) && slices.Contains(p.Capability.Grants, capability.OperatorTokens) || p.Class == capability.CredentialConsole && slices.Contains(p.Capability.Grants, capability.OperatorConsole)
}

// CreateOperatorTeam commits a new team, its first administrator and the operator audit together.
func (o *Operator) CreateOperatorTeam(ctx context.Context, a capability.Authorized, slug, displayName string) (capability.Issued, error) {
	var issued capability.Issued
	err := o.s.inTx(ctx, func(tx *storeTx) error {
		p, err := resolveProvisionAuthorityTx(ctx, tx, a.Principal.Authority, capability.ProofForConsole(a.Principal))
		if err != nil {
			return err
		}
		if !operatorTeamAuthority(p) {
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
		if _, err := tx.execContext(ctx, `INSERT INTO accounts(id,email,name,active_team,created_at,updated_at) VALUES (?,'',?,?,?,?)`, account, displayName, slug, now, now); err != nil {
			return err
		}
		if err := createTeamTx(ctx, tx, account, Team(slug), displayName, auditPrincipalActor(p), tx.now(ctx)); err != nil {
			return err
		}
		tx.createdTeam = &TeamInfo{Slug: Team(slug), CreatedBy: account}
		tx.operatorTeamAdmin = tx.createdTeam
		issued, err = o.s.issue(ctx, tx, operatorTeamAdminRequest(ctx, tx, p, slug, account))
		return err
	})
	if err != nil {
		return capability.Issued{}, err
	}
	return issued, nil
}

// ReissueOperatorTeamAdmin issues a fresh administrator credential to the account an operator created
// a team with. Customer teams are refused: when any member can sign in, operator authority would
// otherwise become a silent way into the team.
func (o *Operator) ReissueOperatorTeamAdmin(ctx context.Context, a capability.Authorized, slug string, revokePrevious bool) (OperatorTeamAdminReissue, error) {
	var out OperatorTeamAdminReissue
	err := o.s.inTx(ctx, func(tx *storeTx) error {
		out = OperatorTeamAdminReissue{Revoked: []string{}}
		if !operatorTeamAuthority(a.Principal) {
			return bindingRefusal()
		}
		if err := ValidateSlug(slug); err != nil {
			return authorityError(apierr.BadRequest, "invalid team slug")
		}
		var account string
		err := tx.QueryRowContext(ctx, `SELECT created_by FROM teams WHERE name=? AND NOT EXISTS (SELECT 1 FROM team_deletions WHERE slug=teams.name)`, slug).Scan(&account)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrUnknownTeam
		}
		if err != nil {
			return err
		}
		var lineage []capability.AuthorityRef
		if err := acquireLockSet(ctx, tx, func() lockSet {
			set := discoverAuthorityLocks(ctx, tx, []capability.AuthorityRef{a.Principal.Authority})
			set.add(lockTeam, true, slug)
			if revokePrevious && set.err == nil {
				lineage, set.err = tx.operator.teamAdminLineageTx(ctx, tx, slug, account)
				for _, ref := range lineage {
					set = discoverAuthorityWrite(ctx, tx, set, ref)
				}
			}
			return set
		}); err != nil {
			return err
		}
		p, err := resolveProvisionAuthorityTx(ctx, tx, a.Principal.Authority, capability.ProofForConsole(a.Principal))
		if err != nil {
			return err
		}
		if !operatorTeamAuthority(p) {
			return bindingRefusal()
		}
		if err := requireHeadlessTeamTx(ctx, tx, slug, account); err != nil {
			return err
		}
		actor := auditPrincipalActor(p)
		// safety: descendants go first so each revocation resolves the chain its lock was taken on.
		for _, ref := range slices.Backward(lineage) {
			live, err := tx.operator.liveAuthorityTx(ctx, tx, ref)
			if err != nil {
				return err
			}
			if !live {
				continue
			}
			if err := tx.operator.revokeAuthorityTx(ctx, tx, ref); err != nil {
				return err
			}
			if err := recordMutationAuditTx(ctx, tx, Team(slug), actor, "authority.revoked", string(ref.Kind)+":"+ref.ID); err != nil {
				return err
			}
			if ref.Kind == capability.AuthorityCapability {
				out.Revoked = append(out.Revoked, ref.ID)
			}
		}
		tx.operatorTeamAdmin = &TeamInfo{Slug: Team(slug), CreatedBy: account}
		out.Issued, err = o.s.issue(ctx, tx, operatorTeamAdminRequest(ctx, tx, p, slug, account))
		if err != nil {
			return err
		}
		return recordMutationAuditTx(ctx, tx, Team(slug), actor, "ReissueOperatorTeamAdmin", account)
	})
	if err != nil {
		return OperatorTeamAdminReissue{}, err
	}
	slices.Reverse(out.Revoked)
	return out, nil
}

func operatorTeamAdminRequest(ctx context.Context, tx *storeTx, p capability.Principal, team, account string) capability.IssueRequest {
	grants := []capability.Grant{}
	for _, scope := range ScopesForRole(RoleOwner) {
		grants = append(grants, capability.Grant(scope))
	}
	expires := tx.now(ctx).Add(adminTokenLifetime)
	return capability.IssueRequest{Kind: capability.Delegate, Provision: &capability.ProvisionSpec{
		Kind: capability.ProvisionTeamAdmin, Authority: &p.Authority, ConsoleProof: capability.ProofForConsole(p),
		Team: team, Subject: capability.Subject{Kind: capability.SubjectAccount, ID: account},
		Grants: grants, Binding: capability.Binding{Kind: capability.BindingNone}, ExpiresAt: &expires,
	}}
}

func requireHeadlessTeamTx(ctx context.Context, tx *storeTx, team, account string) error {
	var role Role
	err := tx.QueryRowContext(ctx, `SELECT role FROM memberships WHERE team=? AND account_id=?`, team, account).Scan(&role)
	if errors.Is(err, sql.ErrNoRows) || err == nil && role != RoleOwner {
		return authorityError(apierr.Conflict, "the team's operator-created account no longer owns it")
	}
	if err != nil {
		return err
	}
	var people int
	// safety: a membership without an account row counts as a person, so unknown state refuses.
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM memberships m WHERE m.team=?
 AND (NOT EXISTS (SELECT 1 FROM accounts a WHERE a.id=m.account_id AND a.email='')
  OR EXISTS (SELECT 1 FROM identities i WHERE i.account_id=m.account_id))`, team).Scan(&people); err != nil {
		return err
	}
	if people != 0 {
		return authorityError(apierr.Conflict, "team has members who sign in; a team owner must issue its credentials")
	}
	return nil
}

func (op *Operator) teamAdminLineageTx(ctx context.Context, tx *storeTx, team, account string) ([]capability.AuthorityRef, error) {
	rows, err := tx.queryContext(ctx, `SELECT prefix FROM capabilities WHERE team=? AND subject_kind='account' AND subject_id=? AND parent_kind IS NULL ORDER BY prefix`, team, account)
	if err != nil {
		return nil, err
	}
	prefixes, err := scanPrefixes(rows)
	if err != nil {
		return nil, err
	}
	var roots []capability.AuthorityRef
	for _, prefix := range prefixes {
		roots = append(roots, capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: prefix})
	}
	return op.authorityLineageTx(ctx, tx, roots)
}

func (op *Operator) liveAuthorityTx(ctx context.Context, tx *storeTx, ref capability.AuthorityRef) (bool, error) {
	now := tx.now(ctx).UnixNano()
	query, args := `SELECT COUNT(*) FROM capabilities WHERE prefix=? AND `+liveCredentialSQL(""), []any{ref.ID, now, now}
	if ref.Kind == capability.AuthoritySession {
		query, args = `SELECT COUNT(*) FROM sessions WHERE hash=? AND revoked_at IS NULL`, []any{ref.ID}
	}
	var n int
	err := tx.QueryRowContext(ctx, query, args...).Scan(&n)
	return n != 0, err
}
