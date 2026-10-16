package store

import (
	"context"
	"encoding/json"
	"reflect"
	"slices"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
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
		var prefix string
		if err := tx.QueryRowContext(ctx, `SELECT prefix FROM capabilities WHERE prefix=? AND COALESCE(team,'')=?`+tx.forUpdate(), a.Target.TokenPrefix, team).Scan(&prefix); err != nil {
			return err
		}
		p, err := managementAuthorityTx(ctx, tx, team, a, "POST", true)
		if err != nil {
			return err
		}
		c, previous, err := capabilityMetadataTx(ctx, tx, prefix)
		if err != nil {
			return err
		}
		if !inventoryOwned(p, team, c, previous.CreatedBy) {
			return bindingRefusal()
		}
		if c.RevokedAt != nil || c.ExpiresAt != nil && !tx.now().Before(*c.ExpiresAt) {
			return authorityError(apierr.LockHeld, "credential has ended or already has a successor")
		}
		if expires == nil {
			expires = c.ExpiresAt
		}
		if expires != nil && !expires.After(tx.now()) {
			return authorityError(apierr.BadRequest, "replacement expiry has passed")
		}
		request, err := rotationRequestTx(ctx, tx, p, c, expires)
		if err != nil {
			return err
		}
		if c.Parent == nil {
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
		deadline := tx.now().Add(grace)
		if c.ExpiresAt != nil && c.ExpiresAt.Before(deadline) {
			deadline = *c.ExpiresAt
		}
		if err := scheduleRotationTx(ctx, tx, c, deadline); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE capabilities SET created_by=? WHERE prefix=? AND COALESCE(team,'')=?`, previous.CreatedBy, issued.Capability.Prefix, team); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO git_credential_machines(team,capability_prefix,enabled_by,enabled_at) SELECT team,?,enabled_by,enabled_at FROM git_credential_machines WHERE team=? AND capability_prefix=? AND enabled_by=?`, issued.Capability.Prefix, team, c.Prefix, previous.CreatedBy); err != nil {
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

func rotationRequestTx(ctx context.Context, tx *storeTx, p capability.Principal, c capability.Capability, expires *time.Time) (capability.IssueRequest, error) {
	if c.Parent != nil {
		spec := capability.DelegateSpec{Parent: *c.Parent, Audience: c.Audience, Grants: slices.Clone(c.Grants), Binding: c.Binding, Repositories: slices.Clone(c.Repositories), Data: c.Data, ExpiresAt: expires}
		return capability.IssueRequest{Kind: capability.Delegate, Delegate: &spec}, nil
	}
	record, err := loadCapabilityRecordTx(ctx, tx, c.Prefix)
	if err != nil {
		return capability.IssueRequest{}, err
	}
	if record.Origin == nil {
		return capability.IssueRequest{}, authorityError(apierr.IntegrityFailure, "credential provisioning origin is unavailable")
	}
	origin := record.Origin
	spec := capability.ProvisionSpec{Kind: origin.Kind, RegistrationID: origin.RegistrationID, Authority: &p.Authority, Team: c.Team, Subject: c.Subject, Grants: slices.Clone(c.Grants), Binding: c.Binding, ReservedPools: slices.Clone(c.ReservedPools), Repositories: slices.Clone(c.Repositories), Data: c.Data, Metered: c.RootMetered, ExpiresAt: expires}
	return capability.IssueRequest{Kind: capability.Delegate, Provision: &spec}, nil
}

func scheduleRotationTx(ctx context.Context, tx *storeTx, c capability.Capability, deadline time.Time) error {
	record, err := loadCapabilityRecordTx(ctx, tx, c.Prefix)
	if err != nil {
		return err
	}
	if record.Value.ExpiresAt != nil && !tx.now().Before(*record.Value.ExpiresAt) {
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
	return advanceAuthGenerationsTx(ctx, tx, []capability.AuthorityRef{{Kind: capability.AuthorityCapability, ID: c.Prefix}}, nil)
}

func validCapabilityOrigin(c capability.Capability, origin CapabilityOrigin) bool {
	if c.Parent != nil || c.Unit != "" || c.Issuance != capability.Delegate {
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
	return old.Team == next.Team && old.Subject == next.Subject && old.Audience == next.Audience && old.RootMetered == next.RootMetered && old.Binding == next.Binding && reflect.DeepEqual(old.Parent, next.Parent) && slices.Equal(old.Grants, next.Grants) && slices.Equal(old.Repositories, next.Repositories) && slices.Equal(old.ReservedPools, next.ReservedPools) && reflect.DeepEqual(old.Data, next.Data)
}
