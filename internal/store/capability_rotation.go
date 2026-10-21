package store

import (
	"context"
	"encoding/json"
	"reflect"
	"slices"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

// RotateCapability replaces an owned tenant credential and commits its grace deadline.
func (t *Tenant) RotateCapability(ctx context.Context, a capability.Authorized, grace time.Duration, expires *time.Time) (capability.Rotation, error) {
	return t.s.rotateCapability(ctx, t.team, a, grace, expires)
}

// RotateCapability replaces a deployment root and commits its grace deadline.
func (o *Operator) RotateCapability(ctx context.Context, a capability.Authorized, grace time.Duration, expires *time.Time) (capability.Rotation, error) {
	return o.s.rotateCapability(ctx, "", a, grace, expires)
}

func (s *Store) rotateCapability(ctx context.Context, team Team, a capability.Authorized, grace time.Duration, expires *time.Time) (capability.Rotation, error) {
	if grace < 0 || grace > 7*24*time.Hour {
		return capability.Rotation{}, authorityError(apierr.BadRequest, "rotation grace must be between zero and seven days")
	}
	var out capability.Rotation
	err := s.inTx(ctx, func(tx *storeTx) error {
		requested, expires := expires, expires
		prefix := a.Target.TokenPrefix
		if team == "" {
			if err := advisoryLockTx(ctx, tx, bootstrapUsedKey); err != nil {
				return err
			}
		}
		if err := acquireLockSet(ctx, tx, func() lockSet {
			set := discoverAuthorityLocks(ctx, tx, []capability.AuthorityRef{a.Principal.Authority, {Kind: capability.AuthorityCapability, ID: prefix}}, prefix)
			if team != "" {
				set.add(lockTeam, true, string(team))
			} else {
				set = discoverSetupAuthorityLocks(ctx, tx, set)
			}
			return set
		}); err != nil {
			return err
		}
		p, err := managementAuthorityTx(ctx, tx, team, a, "POST", true)
		if err != nil {
			return err
		}
		c, previous, err := managedCapabilityMetadataTx(ctx, tx, prefix)
		if err != nil {
			return err
		}
		if !capability.OwnsToken(p, previous) {
			return bindingRefusal()
		}
		if err := rotationOwnerTx(ctx, tx, p, c, previous.CreatedBy); err != nil {
			return err
		}
		if err := machineAdministrationTx(ctx, tx, p, c); err != nil {
			return err
		}
		if c.RevokedAt != nil || c.ExpiresAt != nil && !tx.now(ctx).Before(*c.ExpiresAt) {
			return authorityError(apierr.LockHeld, "credential has ended or already has a successor")
		}
		if expires == nil {
			expires = c.ExpiresAt
		}
		if expires != nil && !expires.After(tx.now(ctx)) {
			return authorityError(apierr.BadRequest, "replacement expiry has passed")
		}
		if !p.DirectAccountSession() && p.Class != capability.CredentialConsole {
			ceiling, err := rotationExpiryCeilingTx(ctx, tx, p.Authority, c.ExpiresAt)
			if err != nil {
				return err
			}
			if requested != nil && ceiling != nil && requested.After(*ceiling) {
				return authorityError(apierr.Forbidden, "only an account or console session can extend a credential's expiry")
			}
			if ceiling != nil && (expires == nil || expires.After(*ceiling)) {
				expires = ceiling
			}
		}
		request, err := rotationRequestTx(ctx, tx, p, c, expires)
		if err != nil {
			return err
		}
		if request.Provision != nil {
			tx.rotatingRoot = &c
		}
		issued, err := s.Issue(ctx, tx, request)
		tx.rotatingRoot = nil
		if err != nil {
			return err
		}
		if !sameRotationScope(c, issued.Capability) {
			return bindingRefusal()
		}
		if _, err := managementAuthorityTx(ctx, tx, team, a, "POST", true); err != nil {
			return err
		}
		deadline := tx.now(ctx).Add(grace)
		if c.ExpiresAt != nil && c.ExpiresAt.Before(deadline) {
			deadline = *c.ExpiresAt
		}
		if err := scheduleRotationTx(ctx, tx, c, deadline); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE capabilities SET created_by=? WHERE prefix=? AND COALESCE(team,'')=?`, previous.CreatedBy, issued.Capability.Prefix, team); err != nil {
			return err
		}
		out = capability.Rotation{Issued: issued, Previous: previous, RevokesAt: deadline}
		return nil
	})
	if err != nil {
		return capability.Rotation{}, err
	}
	return out, nil
}

func rotationOwnerTx(ctx context.Context, tx *storeTx, p capability.Principal, c capability.Capability, creator string) error {
	caller := p.Capability.Subject
	if c.Team == "" || caller.Kind == capability.SubjectAccount && (c.Subject == caller || c.Subject.Kind == capability.SubjectMachine && creator == caller.ID) {
		return nil
	}
	if c.Subject.Kind == capability.SubjectMachine && slices.Contains(p.Capability.Grants, capability.TeamAdmin) {
		facts, err := claimantFactsTx(ctx, tx, []authorityNode{{principal: capability.Principal{Capability: c}}}, runspec.Request{})
		if err != nil {
			return err
		}
		if facts.Shared {
			return nil
		}
	}
	return authorityError(apierr.Forbidden, "only a token's owner can rotate it; a team admin can revoke it instead")
}

func rotationExpiryCeilingTx(ctx context.Context, tx *storeTx, caller capability.AuthorityRef, old *time.Time) (*time.Time, error) {
	chain, err := authorityChainTx(ctx, tx, caller, false)
	if err != nil {
		return nil, err
	}
	ceiling := old
	for _, node := range chain {
		if at := node.principal.Capability.ExpiresAt; at != nil && (ceiling == nil || at.Before(*ceiling)) {
			ceiling = at
		}
	}
	return ceiling, nil
}

func rotationRequestTx(ctx context.Context, tx *storeTx, p capability.Principal, c capability.Capability, expires *time.Time) (capability.IssueRequest, error) {
	record, err := tx.operator.loadCapabilityRecordTx(ctx, tx, c.Prefix)
	if err != nil {
		return capability.IssueRequest{}, err
	}
	if c.Parent != nil && (record.Origin == nil || record.Origin.Kind != capability.ProvisionMember) {
		spec := capability.DelegateSpec{Parent: *c.Parent, Audience: c.Audience, Grants: slices.Clone(c.Grants), Binding: c.Binding, Repositories: slices.Clone(c.Repositories), Data: c.Data, ExpiresAt: expires}
		return capability.IssueRequest{Kind: capability.Delegate, Delegate: &spec}, nil
	}
	if record.Origin == nil {
		return capability.IssueRequest{}, authorityError(apierr.IntegrityFailure, "credential provisioning origin is unavailable")
	}
	origin := record.Origin
	spec := capability.ProvisionSpec{AllowTeamAdmin: slices.Contains(c.Grants, capability.TeamAdmin), Personal: origin.Personal, Kind: origin.Kind, RegistrationID: origin.RegistrationID, Authority: &p.Authority, Team: c.Team, Subject: c.Subject, Grants: slices.Clone(c.Grants), Binding: c.Binding, ReservedPools: slices.Clone(c.ReservedPools), Trust: c.Trust, Repositories: slices.Clone(c.Repositories), Data: c.Data, Metered: c.RootMetered, ExpiresAt: expires}
	return capability.IssueRequest{Kind: capability.Delegate, Provision: &spec}, nil
}

func scheduleRotationTx(ctx context.Context, tx *storeTx, c capability.Capability, deadline time.Time) error {
	record, err := tx.operator.loadCapabilityRecordTx(ctx, tx, c.Prefix)
	if err != nil {
		return err
	}
	if record.Value.ExpiresAt != nil && !tx.now(ctx).Before(*record.Value.ExpiresAt) {
		return authorityError(apierr.LockHeld, "credential expired during rotation")
	}
	record.Value.RevokedAt = &deadline
	body, err := json.Marshal(record)
	if err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `UPDATE capabilities SET authority_json=?,revoked_at=? WHERE prefix=? AND COALESCE(team,'')=? AND revoked_at IS NULL`, body, deadline.UnixNano(), c.Prefix, c.Team)
	if err := credentialRowChanged(result, err); err != nil {
		return err
	}
	return nil
}

func validCapabilityOrigin(c capability.Capability, origin CapabilityOrigin) bool {
	if origin.Personal && (origin.Kind != capability.ProvisionMachine || origin.Requirement == nil) {
		return false
	}
	if requirement := origin.Requirement; requirement != nil {
		if requirement.Account == "" || requirement.Epoch < 0 || requirement.Grant != "" && !slices.Contains(capability.Grants(), requirement.Grant) {
			return false
		}
	}
	if origin.Kind == capability.ProvisionMember && (c.Parent == nil || c.Parent.Kind != capability.AuthoritySession || c.ExpiresAt == nil || origin.Requirement == nil) {
		return false
	}
	if (c.Parent != nil && (origin.Kind != capability.ProvisionMember || c.Parent.Kind != capability.AuthoritySession)) || c.Unit != "" || c.Issuance != capability.Delegate {
		return false
	}
	switch origin.Kind {
	case capability.ProvisionMember, capability.ProvisionTeamAdmin:
		return c.Subject.Kind == capability.SubjectAccount && c.Team != "" && c.Binding.Kind == capability.BindingNone && origin.RegistrationID == ""
	case capability.ProvisionBootstrap:
		return c.Subject.Kind == capability.SubjectOperator && c.Team == "" && c.Binding.Kind == capability.BindingNone
	case capability.ProvisionMachine:
		return c.Subject.Kind == capability.SubjectMachine && c.Team != "" && c.Binding.Kind == capability.BindingNone && origin.RegistrationID != ""
	case capability.ProvisionService:
		return c.Subject.Kind == capability.SubjectService && c.Team == "" && c.Binding.Kind == capability.BindingNone && origin.RegistrationID != ""
	case capability.ProvisionActions:
		return c.Subject.Kind == capability.SubjectMachine && c.Team != "" && c.Binding.Kind == capability.BindingPush && origin.RegistrationID != ""
	}
	return false
}

func sameRotationScope(old, next capability.Capability) bool {
	return old.Team == next.Team && old.Subject == next.Subject && old.Audience == next.Audience && old.RootMetered == next.RootMetered && old.Binding == next.Binding && reflect.DeepEqual(old.Parent, next.Parent) && slices.Equal(old.Grants, next.Grants) && slices.Equal(old.Repositories, next.Repositories) && slices.Equal(old.ReservedPools, next.ReservedPools) && old.Trust == next.Trust && reflect.DeepEqual(old.Data, next.Data)
}
