package store

import (
	"context"
	"database/sql"
	"errors"
	"slices"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

func provisionCapabilityTx(ctx context.Context, tx *storeTx, spec capability.ProvisionSpec) (capability.Capability, error) {
	if spec.Kind == capability.ProvisionService {
		if err := advisoryLockTx(ctx, tx, bootstrapUsedKey); err != nil {
			return capability.Capability{}, err
		}
	}
	if err := capability.ValidateProvisionSeed(spec); err != nil {
		return capability.Capability{}, err
	}
	c := capability.Capability{
		Team: spec.Team, Subject: spec.Subject, Audience: capability.Controller,
		Grants: slices.Clone(spec.Grants), Binding: spec.Binding, Issuance: capability.Delegate,
		ExpiresAt: spec.ExpiresAt, ReservedPools: slices.Clone(spec.ReservedPools),
		Repositories: slices.Clone(spec.Repositories), Data: spec.Data, RootMetered: spec.Metered,
	}
	if tx.rotatingRoot != nil && !sameRotationScope(*tx.rotatingRoot, c) {
		return capability.Capability{}, bindingRefusal()
	}
	if spec.Kind == capability.ProvisionBootstrap && tx.rotatingRoot == nil {
		if err := checkBootstrapGateTx(ctx, tx); err != nil {
			return capability.Capability{}, err
		}
	}
	if err := checkProvisionOriginTx(ctx, tx, spec, false); err != nil {
		return capability.Capability{}, err
	}
	if spec.Kind == capability.ProvisionActions {
		if err := (&Tenant{s: tx.store, team: Team(spec.Team)}).lockTeamTx(ctx, tx); err != nil {
			return capability.Capability{}, err
		}
	}
	if spec.Kind != capability.ProvisionMember && spec.Kind != capability.ProvisionTeamAdmin && !(tx.rotatingRoot != nil && spec.Kind == capability.ProvisionBootstrap) {
		enrollment := spec.Kind == capability.ProvisionMachine || spec.Kind == capability.ProvisionService
		if tx.store.provisions == nil && !enrollment && !(spec.Kind == capability.ProvisionBootstrap && spec.Seed != "") {
			return capability.Capability{}, authorityError(apierr.Unavailable, "root origin verification is not configured")
		}
		if tx.store.provisions != nil {
			if err := tx.store.provisions.AuthorizeProvision(ctx, tx, spec); err != nil {
				return capability.Capability{}, err
			}
		}
		if enrollment {
			if err := authorizeEnrollmentTx(ctx, tx, spec); err != nil {
				return capability.Capability{}, err
			}
		}
		if err := checkProvisionOriginTx(ctx, tx, spec, true); err != nil {
			return capability.Capability{}, err
		}
	}
	if spec.Kind == capability.ProvisionActions {
		if err := checkActionsEnrollmentTx(ctx, tx, spec); err != nil {
			return capability.Capability{}, err
		}
	}
	return c, nil
}

func checkProvisionOriginTx(ctx context.Context, tx *storeTx, spec capability.ProvisionSpec, lockRegistry bool) error {
	if spec.ExpiresAt != nil && !spec.ExpiresAt.After(tx.now()) {
		return authorityError(apierr.Forbidden, "root expiry has passed")
	}
	var p capability.Principal
	if spec.Authority != nil {
		var err error
		p, err = resolveProvisionAuthorityTx(ctx, tx, *spec.Authority, spec.ConsoleProof)
		if err != nil {
			return err
		}
	}
	c := p.Capability
	if spec.Personal && (spec.Kind != capability.ProvisionMachine || !p.DirectAccountSession() || c.Subject.Kind != capability.SubjectAccount || !slices.Contains(c.Grants, capability.RunsRead)) {
		return bindingRefusal()
	}
	deployment := spec.Authority != nil && (capability.DeploymentAuthority(p) && slices.Contains(c.Grants, capability.OperatorTokens) || p.Class == capability.CredentialConsole && slices.Contains(c.Grants, capability.OperatorConsole))
	if spec.Authority != nil && !deployment && c.Team != spec.Team {
		return bindingRefusal()
	}
	bootstrap := spec.Kind == capability.ProvisionBootstrap
	for _, grant := range spec.Grants {
		if grant == capability.OperatorConsole || (!bootstrap && !deployment && (grant == capability.ClaimsLaunch || strings.HasPrefix(string(grant), "operator."))) {
			return authorityError(apierr.Forbidden, "root origin cannot issue deployment or console authority")
		}
	}
	if spec.Metered && !deployment && tx.rotatingRoot == nil {
		return authorityError(apierr.Forbidden, "metering requires deployment authority")
	}
	switch spec.Kind {
	case capability.ProvisionMember:
		return checkMemberProvision(ctx, tx, spec, p)
	case capability.ProvisionTeamAdmin:
		if tx.rotatingRoot != nil {
			return checkMemberProvision(ctx, tx, spec, p)
		}
		if !deployment || tx.createdTeam == nil || spec.Team != string(tx.createdTeam.Slug) || spec.Subject != (capability.Subject{Kind: capability.SubjectAccount, ID: tx.createdTeam.CreatedBy}) || spec.Metered || spec.ExpiresAt != nil || len(spec.ReservedPools) != 0 || spec.Repositories != nil || spec.Binding != (capability.Binding{Kind: capability.BindingNone}) || spec.Data != nil || spec.RegistrationID != "" {
			return bindingRefusal()
		}
		allowed := ScopesForRole(RoleOwner)
		if len(spec.Grants) != len(allowed) {
			return bindingRefusal()
		}
		for i, grant := range spec.Grants {
			if string(grant) != allowed[i] {
				return bindingRefusal()
			}
		}
		return nil
	case capability.ProvisionBootstrap:
		if tx.rotatingRoot != nil {
			if !deployment {
				return bindingRefusal()
			}
		} else if spec.Authority != nil {
			return bindingRefusal()
		}
		if spec.Team != "" || spec.Subject.Kind != capability.SubjectOperator || spec.Binding.Kind != capability.BindingNone || spec.Data != nil || len(spec.ReservedPools) != 0 || len(spec.Repositories) != 0 {
			return bindingRefusal()
		}
	case capability.ProvisionMachine:
		account := spec.Authority != nil && p.DirectAccountSession() && c.Team == spec.Team && c.Audience == capability.Controller
		if account && !spec.Personal && !slices.Contains(c.Grants, capability.TeamAdmin) {
			return bindingRefusal()
		}
		if spec.Team == "" || spec.Subject.Kind != capability.SubjectMachine || spec.Binding.Kind != capability.BindingNone || (!account && !deployment) {
			return bindingRefusal()
		}
		for _, grant := range spec.Grants {
			if grant != capability.NodesClaim && grant != capability.TriggersClaim {
				return authorityError(apierr.Forbidden, "machine roots require claim grants")
			}
		}
		if !deployment && len(spec.ReservedPools) != 0 && !slices.Contains(c.Grants, capability.TeamAdmin) {
			return authorityError(apierr.Forbidden, "reserved pools require team administration")
		}
	case capability.ProvisionService:
		if !deployment || spec.Team != "" || spec.Subject.Kind != capability.SubjectService || spec.Binding.Kind != capability.BindingNone || spec.Data != nil || len(spec.ReservedPools) != 0 || len(spec.Repositories) != 0 {
			return bindingRefusal()
		}
	case capability.ProvisionActions:
		push := spec.Binding.Push
		if spec.Team == "" || spec.Subject.Kind != capability.SubjectMachine || spec.Binding.Kind != capability.BindingPush || push == nil || push.RepositoryKey == "" || push.Ref == "" || push.SHA == "" || push.WorkflowRunID == "" || spec.ExpiresAt == nil || !slices.Equal(spec.Repositories, []string{push.RepositoryKey}) {
			return bindingRefusal()
		}
	default:
		return authorityError(apierr.BadRequest, "unknown root origin")
	}
	if spec.Subject.ID == "" || (spec.RegistrationID == "" && (spec.Kind == capability.ProvisionMachine || spec.Kind == capability.ProvisionService)) {
		return authorityError(apierr.BadRequest, "root origin requires its verified identity")
	}
	binding := spec.Binding
	if spec.Kind == capability.ProvisionActions {
		binding.Push = nil
	}
	if binding != (capability.Binding{Kind: spec.Binding.Kind}) {
		return bindingRefusal()
	}
	if spec.Team != "" {
		var team string
		var membersCanAddMachines bool
		lock := ""
		if lockRegistry {
			lock = tx.forShare()
		}
		err := tx.QueryRowContext(ctx, `SELECT name,members_can_add_machines FROM teams WHERE name=? AND NOT EXISTS (SELECT 1 FROM team_deletions WHERE slug=teams.name AND state=?)`+lock, spec.Team, TeamDeletionPending).Scan(&team, &membersCanAddMachines)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrUnknownTeam
		}
		if err == nil && spec.Personal && !membersCanAddMachines && !slices.Contains(c.Grants, capability.TeamAdmin) {
			return authorityError(apierr.Forbidden, "personal machine enrollment requires team administration while members_can_add_machines is off")
		}
		return err
	}
	return nil
}

func checkMemberProvision(ctx context.Context, tx *storeTx, spec capability.ProvisionSpec, p capability.Principal) error {
	c := p.Capability
	session := p.DirectAccountSession() || tx.rotatingRoot != nil && p.Class == capability.CredentialBearer && c.Unit == "" && c.Binding.Kind == capability.BindingNone
	owner := c.Subject == spec.Subject || tx.rotatingRoot != nil && c.Subject.Kind == capability.SubjectAccount && slices.Contains(c.Grants, capability.TeamAdmin)
	if spec.Authority == nil || !session || !owner || spec.Subject.Kind != capability.SubjectAccount || spec.Team != c.Team || spec.Metered || len(spec.ReservedPools) != 0 || spec.Binding != (capability.Binding{Kind: capability.BindingNone}) || spec.Data != nil || len(spec.Repositories) != 0 || spec.RegistrationID != "" {
		return authorityError(apierr.Forbidden, "member provisioning exceeds current account authority")
	}
	allowed := c.Grants
	if tx.rotatingRoot != nil {
		var role Role
		if err := tx.QueryRowContext(ctx, `SELECT role FROM memberships WHERE team=? AND account_id=?`+tx.forShare(), spec.Team, spec.Subject.ID).Scan(&role); err != nil {
			return err
		}
		if !role.Valid() {
			return bindingRefusal()
		}
		scopes := ScopesForRole(role)
		allowed = slices.DeleteFunc(slices.Clone(allowed), func(g capability.Grant) bool { return !slices.Contains(scopes, string(g)) })
	}
	for _, grant := range spec.Grants {
		if !slices.Contains(allowed, grant) {
			return authorityError(apierr.Forbidden, "member provisioning exceeds current account grants")
		}
	}
	return nil
}

func recordProvisionCreatorTx(ctx context.Context, tx *storeTx, spec capability.ProvisionSpec, c capability.Capability) error {
	if spec.Authority == nil {
		return nil
	}
	p, err := resolveProvisionAuthorityTx(ctx, tx, *spec.Authority, spec.ConsoleProof)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE capabilities SET created_by=? WHERE prefix=? AND COALESCE(team,'')=?`, p.Capability.Subject.ID, c.Prefix, c.Team)
	return err
}
