package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

func capabilityMetadataTx(ctx context.Context, tx *storeTx, prefix string) (capability.Metadata, error) {
	c, err := loadCapabilityTx(ctx, tx, prefix)
	if err != nil {
		return capability.Metadata{}, err
	}
	m := capability.Metadata{Prefix: c.Prefix, Team: c.Team, Subject: c.Subject, Audience: c.Audience, Grants: c.Grants, IssuedAt: c.IssuedAt, ExpiresAt: c.ExpiresAt, RevokedAt: c.RevokedAt, Metered: c.RootMetered}
	err = tx.QueryRowContext(ctx, `SELECT created_by FROM capabilities WHERE prefix=?`, prefix).Scan(&m.CreatedBy)
	return m, err
}

func managementPrincipalTx(ctx context.Context, tx *storeTx, team Team, a capability.Authorized) (capability.Principal, error) {
	p, err := resolveProvisionAuthorityTx(ctx, tx, a.Principal.Authority, capability.ProofForConsole(a.Principal))
	if err != nil {
		return p, err
	}
	if p.Capability.Binding.Kind != capability.BindingNone || p.Capability.Unit != "" {
		return p, bindingRefusal()
	}
	if team == "" {
		if !capability.OwnsToken(p, capability.Metadata{Team: ""}) {
			return p, bindingRefusal()
		}
	} else if p.Capability.Team != string(team) || p.Capability.Subject.Kind != capability.SubjectAccount {
		return p, bindingRefusal()
	}
	return p, nil
}

func listCapabilities(ctx context.Context, s *Store, team Team, a capability.Authorized, filter capability.InventoryFilter) ([]capability.Metadata, error) {
	tx, err := s.beginSnapshotReadTx(ctx)
	if err != nil {
		return nil, err
	}
	defer rollbackOrLog(tx)
	p, err := managementPrincipalTx(ctx, tx, team, a)
	if err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT prefix FROM capabilities WHERE COALESCE(team,'')=? ORDER BY created_at,prefix`, team)
	if err != nil {
		return nil, err
	}
	var prefixes []string
	for rows.Next() {
		var prefix string
		if err := rows.Scan(&prefix); err != nil {
			rows.Close()
			return nil, err
		}
		prefixes = append(prefixes, prefix)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return nil, err
	}
	out := []capability.Metadata{}
	for _, prefix := range prefixes {
		c, err := loadCapabilityTx(ctx, tx, prefix)
		if err != nil {
			return nil, err
		}
		if c.Binding.Kind != capability.BindingNone || c.Unit != "" {
			continue
		}
		m, err := capabilityMetadataTx(ctx, tx, prefix)
		if err != nil {
			return nil, err
		}
		if !capability.OwnsToken(p, m) || filter.SubjectKind != "" && filter.SubjectKind != m.Subject.Kind || !filter.IncludeRevoked && m.RevokedAt != nil && !tx.now().Before(*m.RevokedAt) {
			continue
		}
		out = append(out, m)
	}
	return out, tx.Commit()
}

func readManagedCapabilityTx(ctx context.Context, tx *storeTx, team Team, a capability.Authorized) (capability.Metadata, error) {
	p, err := managementPrincipalTx(ctx, tx, team, a)
	if err != nil {
		return capability.Metadata{}, err
	}
	m, err := capabilityMetadataTx(ctx, tx, a.Target.TokenPrefix)
	if errors.Is(err, sql.ErrNoRows) {
		return m, ErrNotFound
	}
	if err != nil {
		return m, err
	}
	c, err := loadCapabilityTx(ctx, tx, m.Prefix)
	if err != nil {
		return m, err
	}
	if m.Team != string(team) || c.Binding.Kind != capability.BindingNone || c.Unit != "" || !capability.OwnsToken(p, m) {
		return m, bindingRefusal()
	}
	return m, nil
}

func readManagedCapability(ctx context.Context, s *Store, team Team, a capability.Authorized) (capability.Metadata, error) {
	tx, err := s.beginSnapshotReadTx(ctx)
	if err != nil {
		return capability.Metadata{}, err
	}
	defer rollbackOrLog(tx)
	m, err := readManagedCapabilityTx(ctx, tx, team, a)
	if err != nil {
		return m, err
	}
	return m, tx.Commit()
}

func rotateCapability(ctx context.Context, s *Store, team Team, a capability.Authorized, grace time.Duration, expires *time.Time) (capability.Rotation, error) {
	var out capability.Rotation
	err := s.inTx(ctx, func(tx *storeTx) error {
		if grace < 0 || grace > 7*24*time.Hour || expires != nil && !expires.After(tx.now()) {
			return authorityError(apierr.BadRequest, "invalid rotation lifetime")
		}
		m, err := readManagedCapabilityTx(ctx, tx, team, a)
		if err != nil {
			return err
		}
		if m.RevokedAt != nil || m.ExpiresAt != nil && !tx.now().Before(*m.ExpiresAt) {
			return bindingRefusal()
		}
		c, err := loadCapabilityTx(ctx, tx, m.Prefix)
		if err != nil {
			return err
		}
		c.Prefix = ""
		c.Digest = [32]byte{}
		c.RevokedAt = nil
		if expires != nil {
			c.ExpiresAt = expires
		}
		if c.Parent != nil {
			parent, err := tx.ResolveAuthority(ctx, *c.Parent)
			if err != nil {
				return err
			}
			if err := capability.ValidateDelegation(parent, capability.DelegateSpec{Parent: *c.Parent, Audience: c.Audience, Grants: c.Grants, Binding: c.Binding, Repositories: c.Repositories, Data: c.Data, ExpiresAt: c.ExpiresAt}, tx.now()); err != nil {
				return err
			}
		}
		issued, err := persistIssuedCapabilityTx(ctx, tx, c, "")
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE capabilities SET created_by=? WHERE prefix=?`, m.CreatedBy, issued.Capability.Prefix); err != nil {
			return err
		}
		deadline := tx.now().Add(grace)
		old, err := loadCapabilityTx(ctx, tx, m.Prefix)
		if err != nil {
			return err
		}
		old.RevokedAt = &deadline
		body, err := json.Marshal(CapabilityRecord{Version: 1, Value: old})
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE capabilities SET revoked_at=?,authority_json=? WHERE prefix=?`, deadline.UnixNano(), body, m.Prefix); err != nil {
			return err
		}
		if err = advanceAuthGenerationsTx(ctx, tx, []capability.AuthorityRef{{Kind: capability.AuthorityCapability, ID: m.Prefix}}, nil); err != nil {
			return err
		}
		out = capability.Rotation{Issued: issued, Previous: m, RevokesAt: deadline}
		return nil
	})
	return out, err
}

// ListCapabilities returns only metadata owned by the current tenant principal.
func (t *Tenant) ListCapabilities(ctx context.Context, a capability.Authorized, f capability.InventoryFilter) ([]capability.Metadata, error) {
	return listCapabilities(ctx, t.s, t.team, a, f)
}

// ReadCapability revalidates ownership in its snapshot.
func (t *Tenant) ReadCapability(ctx context.Context, a capability.Authorized) (capability.Metadata, error) {
	return readManagedCapability(ctx, t.s, t.team, a)
}

// RotateCapability atomically issues a replacement and schedules the old credential's revocation.
func (t *Tenant) RotateCapability(ctx context.Context, a capability.Authorized, g time.Duration, e *time.Time) (capability.Rotation, error) {
	return rotateCapability(ctx, t.s, t.team, a, g, e)
}

// ListCapabilities returns deployment credential metadata under current operational authority.
func (o *Operator) ListCapabilities(ctx context.Context, a capability.Authorized, f capability.InventoryFilter) ([]capability.Metadata, error) {
	return listCapabilities(ctx, o.s, "", a, f)
}

// ReadCapability revalidates deployment authority in its snapshot.
func (o *Operator) ReadCapability(ctx context.Context, a capability.Authorized) (capability.Metadata, error) {
	return readManagedCapability(ctx, o.s, "", a)
}

// RotateCapability atomically rotates an operational deployment credential.
func (o *Operator) RotateCapability(ctx context.Context, a capability.Authorized, g time.Duration, e *time.Time) (capability.Rotation, error) {
	return rotateCapability(ctx, o.s, "", a, g, e)
}

// Team identifies deployment-wide credential management.
func (o *Operator) Team() Team { return "" }
