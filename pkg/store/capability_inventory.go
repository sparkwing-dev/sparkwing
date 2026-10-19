package store

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"slices"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

// PrefixLen is the length of a displayed credential lookup prefix.
const PrefixLen = 12

// Team reports the deployment scope.
func (o *Operator) Team() Team { return "" }

// ListCapabilities reads controller credential metadata within current tenant ownership.
func (t *Tenant) ListCapabilities(ctx context.Context, a capability.Authorized, filter capability.InventoryFilter) ([]capability.Metadata, error) {
	return t.s.listCapabilities(ctx, t.team, a, filter)
}

// ListCapabilities reads deployment service and operator root metadata.
func (o *Operator) ListCapabilities(ctx context.Context, a capability.Authorized, filter capability.InventoryFilter) ([]capability.Metadata, error) {
	return o.s.listCapabilities(ctx, "", a, filter)
}

// ReadCapability reads one controller credential within current tenant ownership.
func (t *Tenant) ReadCapability(ctx context.Context, a capability.Authorized) (capability.Metadata, error) {
	return t.s.readCapability(ctx, t.team, a)
}

// ReadCapability reads one deployment service or operator root.
func (o *Operator) ReadCapability(ctx context.Context, a capability.Authorized) (capability.Metadata, error) {
	return o.s.readCapability(ctx, "", a)
}

func (s *Store) listCapabilities(ctx context.Context, team Team, a capability.Authorized, filter capability.InventoryFilter) ([]capability.Metadata, error) {
	if filter.SubjectKind != "" && !slices.Contains([]capability.SubjectKind{capability.SubjectAccount, capability.SubjectMachine, capability.SubjectService, capability.SubjectOperator}, filter.SubjectKind) {
		return nil, authorityError(apierr.BadRequest, "unknown credential subject kind")
	}
	tx, err := s.beginSnapshotReadTx(ctx)
	if err != nil {
		return nil, err
	}
	defer rollbackOrLog(tx)
	current, err := managementAuthorityTx(ctx, tx, team, a, "GET", false)
	if err != nil {
		return nil, err
	}
	query := `SELECT prefix FROM capabilities WHERE COALESCE(team,'')=? AND audience='controller' AND bind_kind='none' AND unit_kind IS NULL`
	args := []any{string(team)}
	if team == "" {
		query += ` AND subject_kind IN ('service','operator') AND parent_id IS NULL`
	} else {
		query += ` AND subject_kind IN ('account','machine')`
		if !slices.Contains(current.Capability.Grants, capability.TeamAdmin) {
			query += ` AND (created_by=? OR (subject_kind='account' AND subject_id=?))`
			args = append(args, current.Capability.Subject.ID, current.Capability.Subject.ID)
		}
	}
	if filter.SubjectKind != "" {
		query += ` AND subject_kind=?`
		args = append(args, string(filter.SubjectKind))
	}
	if !filter.IncludeRevoked {
		query += ` AND (revoked_at IS NULL OR revoked_at>?) AND (expires_at IS NULL OR expires_at>?)`
		args = append(args, tx.now().UnixNano(), tx.now().UnixNano())
	}
	rows, err := tx.QueryContext(ctx, query+` ORDER BY created_at DESC,prefix`, args...)
	if err != nil {
		return nil, err
	}
	prefixes, err := scanPrefixes(rows)
	if err != nil {
		return nil, err
	}
	out := []capability.Metadata{}
	for _, prefix := range prefixes {
		c, row, err := capabilityMetadataTx(ctx, tx, prefix)
		if err != nil {
			return nil, err
		}
		if !inventoryOwned(current, team, c, row.CreatedBy) {
			return nil, bindingRefusal()
		}
		out = append(out, row)
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Store) readCapability(ctx context.Context, team Team, a capability.Authorized) (capability.Metadata, error) {
	tx, err := s.beginSnapshotReadTx(ctx)
	if err != nil {
		return capability.Metadata{}, err
	}
	defer rollbackOrLog(tx)
	current, err := managementAuthorityTx(ctx, tx, team, a, "GET", true)
	if err != nil {
		return capability.Metadata{}, err
	}
	c, row, err := capabilityMetadataTx(ctx, tx, a.Target.TokenPrefix)
	if err != nil {
		return capability.Metadata{}, err
	}
	if !inventoryOwned(current, team, c, row.CreatedBy) {
		return capability.Metadata{}, bindingRefusal()
	}
	if err := tx.Commit(); err != nil {
		return capability.Metadata{}, err
	}
	return row, nil
}

func managementAuthorityTx(ctx context.Context, tx *storeTx, team Team, a capability.Authorized, method string, detail bool) (capability.Principal, error) {
	if a.Target.Team != string(team) || a.Target.RunID != "" || a.Target.NodeID != "" || detail != (a.Target.TokenPrefix != "") || a.ResultReplay || a.LogTailAttempt != 0 {
		return capability.Principal{}, bindingRefusal()
	}
	path := "/api/v1/tokens"
	if team == "" {
		path = "/api/v1/operator/tokens"
	}
	if detail {
		path += "/{token}"
	}
	if method == "POST" {
		path += "/rotate"
	}
	var policy capability.RoutePolicy
	for _, route := range capability.Routes() {
		if route.Method == method && route.Path == path {
			policy = route
			break
		}
	}
	selected := -1
	for i, rule := range policy.Access {
		if reflect.DeepEqual(rule, a.Access) {
			selected = i
			break
		}
	}
	if selected < 0 {
		return capability.Principal{}, bindingRefusal()
	}
	p, err := resolveProvisionAuthorityTx(ctx, tx, a.Principal.Authority, capability.ProofForConsole(a.Principal))
	if err != nil {
		return capability.Principal{}, err
	}
	authenticated := capability.Authenticated{Principal: p, Method: method, Pattern: path, Live: []capability.LiveAccess{{RuleIndex: selected}}}
	fresh, err := capability.Authorize(ctx, tx, policy, authenticated, a.Target)
	if err != nil {
		return capability.Principal{}, err
	}
	return fresh.Principal, nil
}

func inventoryOwned(p capability.Principal, team Team, c capability.Capability, creator string) bool {
	if c.Team != string(team) || c.Audience != capability.Controller || c.Binding != (capability.Binding{Kind: capability.BindingNone}) || c.Unit != "" || c.Purpose != "" || c.Issuance != capability.Delegate {
		return false
	}
	if team == "" {
		return c.Parent == nil && (c.Subject.Kind == capability.SubjectService || c.Subject.Kind == capability.SubjectOperator)
	}
	return (c.Subject.Kind == capability.SubjectAccount || c.Subject.Kind == capability.SubjectMachine) && (slices.Contains(p.Capability.Grants, capability.TeamAdmin) || creator == p.Capability.Subject.ID || c.Subject.Kind == capability.SubjectAccount && c.Subject.ID == p.Capability.Subject.ID)
}

func capabilityMetadataTx(ctx context.Context, tx *storeTx, prefix string) (capability.Capability, capability.Metadata, error) {
	c, err := loadCapabilityTx(ctx, tx, prefix)
	if err != nil {
		return capability.Capability{}, capability.Metadata{}, err
	}
	row := capability.Metadata{Prefix: c.Prefix, Team: c.Team, Subject: c.Subject, Audience: c.Audience, Grants: slices.Clone(c.Grants), IssuedAt: c.IssuedAt, ExpiresAt: c.ExpiresAt, RevokedAt: c.RevokedAt, Metered: c.RootMetered}
	var last sql.NullInt64
	err = tx.QueryRowContext(ctx, `SELECT created_by,last_used_at FROM capabilities WHERE prefix=? AND COALESCE(team,'')=?`, prefix, c.Team).Scan(&row.CreatedBy, &last)
	if errors.Is(err, sql.ErrNoRows) {
		err = ErrNotFound
	}
	if err != nil {
		return capability.Capability{}, capability.Metadata{}, err
	}
	row.LastUsedAt = stateTime(last)
	return c, row, nil
}
