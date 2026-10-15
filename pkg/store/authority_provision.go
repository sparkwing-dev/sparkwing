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
	if err := capability.ValidateProvisionSeed(spec); err != nil {
		return capability.Capability{}, err
	}
	if err := checkProvisionOriginTx(ctx, tx, spec, false); err != nil {
		return capability.Capability{}, err
	}
	if spec.Seed != "" {
		if err := consumeBootstrapGateTx(ctx, tx, spec); err != nil {
			return capability.Capability{}, err
		}
	} else if spec.Kind != capability.ProvisionMember {
		if tx.store.provisions == nil {
			return capability.Capability{}, authorityError(apierr.Unavailable, "root origin verification is not configured")
		}
		if err := tx.store.provisions.AuthorizeProvision(ctx, tx, spec); err != nil {
			return capability.Capability{}, err
		}
		if err := checkProvisionOriginTx(ctx, tx, spec, true); err != nil {
			return capability.Capability{}, err
		}
	}
	return capability.Capability{
		Team: spec.Team, Subject: spec.Subject, Audience: capability.Controller,
		Grants: slices.Clone(spec.Grants), Binding: spec.Binding, Issuance: capability.Delegate,
		ExpiresAt: spec.ExpiresAt, ReservedPools: slices.Clone(spec.ReservedPools),
		Repositories: slices.Clone(spec.Repositories), Data: spec.Data, RootMetered: spec.Metered,
	}, nil
}

func checkProvisionOriginTx(ctx context.Context, tx *storeTx, spec capability.ProvisionSpec, lockRegistry bool) error {
	if spec.ExpiresAt != nil && !spec.ExpiresAt.After(tx.now()) {
		return authorityError(apierr.Forbidden, "root expiry has passed")
	}
	var p capability.Principal
	if spec.Authority != nil {
		var err error
		p, err = tx.ResolveAuthority(ctx, *spec.Authority)
		if err != nil {
			return err
		}
	}
	c := p.Capability
	deployment := spec.Authority != nil && c.Team == "" && c.Binding.Kind == capability.BindingNone && c.Audience == capability.Controller && (c.Subject.Kind == capability.SubjectOperator || c.Subject.Kind == capability.SubjectService) && slices.Contains(c.Grants, capability.OperatorTokens)
	if spec.Authority != nil && !deployment && c.Team != spec.Team {
		return bindingRefusal()
	}
	bootstrap := spec.Kind == capability.ProvisionBootstrap
	for _, grant := range spec.Grants {
		if grant == capability.OperatorConsole || (!bootstrap && !deployment && (grant == capability.ClaimsLaunch || strings.HasPrefix(string(grant), "operator."))) {
			return authorityError(apierr.Forbidden, "root origin cannot issue deployment or console authority")
		}
	}
	if spec.Metered && !deployment {
		return authorityError(apierr.Forbidden, "metering requires deployment authority")
	}
	switch spec.Kind {
	case capability.ProvisionMember:
		return checkMemberProvision(spec, p)
	case capability.ProvisionBootstrap:
		if spec.Authority != nil || spec.Team != "" || spec.Subject.Kind != capability.SubjectOperator || spec.Binding.Kind != capability.BindingNone || spec.Data != nil || len(spec.ReservedPools) != 0 || len(spec.Repositories) != 0 {
			return bindingRefusal()
		}
	case capability.ProvisionMachine:
		account := spec.Authority != nil && p.DirectAccountSession() && c.Team == spec.Team && c.Audience == capability.Controller
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
		lock := ""
		if lockRegistry {
			lock = tx.forShare()
		}
		err := tx.QueryRowContext(ctx, `SELECT name FROM teams WHERE name=? AND NOT EXISTS (SELECT 1 FROM team_deletions WHERE slug=teams.name AND state=?)`+lock, spec.Team, TeamDeletionPending).Scan(&team)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrUnknownTeam
		}
		return err
	}
	return nil
}

func checkMemberProvision(spec capability.ProvisionSpec, p capability.Principal) error {
	c := p.Capability
	if spec.Authority == nil || !p.DirectAccountSession() || c.Subject != spec.Subject || spec.Team != c.Team || spec.Metered || len(spec.ReservedPools) != 0 || spec.Binding != (capability.Binding{Kind: capability.BindingNone}) || spec.Data != nil || len(spec.Repositories) != 0 || spec.RegistrationID != "" {
		return authorityError(apierr.Forbidden, "member provisioning exceeds session authority")
	}
	for _, grant := range spec.Grants {
		if !slices.Contains(c.Grants, grant) {
			return authorityError(apierr.Forbidden, "member provisioning exceeds session grants")
		}
	}
	return nil
}

func recordProvisionCreatorTx(ctx context.Context, tx *storeTx, spec capability.ProvisionSpec, c capability.Capability) error {
	if spec.Authority == nil {
		return nil
	}
	p, err := tx.ResolveAuthority(ctx, *spec.Authority)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE capabilities SET created_by=? WHERE prefix=?`, p.Capability.Subject.ID, c.Prefix)
	return err
}
