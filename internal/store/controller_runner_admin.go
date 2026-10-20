package store

import (
	"context"
	"errors"
	"slices"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

func checkTeamAdministrationTx(ctx context.Context, tx *storeTx, team Team, actor Actor) (capability.Principal, error) {
	p, err := resolveActorTx(ctx, tx, actor)
	if err != nil {
		return p, err
	}
	c := p.Capability
	if c.Team != string(team) || c.Binding.Kind != capability.BindingNone || c.Audience != capability.Controller || (c.Subject.Kind != capability.SubjectAccount && c.Subject.Kind != capability.SubjectLocal) || !slices.Contains(c.Grants, capability.TeamAdmin) {
		return p, authorityError(apierr.Forbidden, "current team administration is required")
	}
	return p, nil
}

// DeleteRunner revokes every root registered under this tenant's machine identity.
func (t *Tenant) DeleteRunner(ctx context.Context, actor Actor, name string) (bool, error) {
	if name == "" {
		return false, authorityError(apierr.BadRequest, "runner name is required")
	}
	tx, err := t.s.beginTx(ctx)
	if err != nil {
		return false, err
	}
	defer rollbackOrLog(tx)
	if _, err := checkTeamAdministrationTx(ctx, tx, t.team, actor); err != nil {
		return false, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT prefix FROM capabilities WHERE team=? AND subject_kind='machine' AND subject_id=? AND bind_kind='none' AND parent_id IS NULL ORDER BY prefix`+tx.forUpdate(), t.team, name)
	if err != nil {
		return false, err
	}
	var prefixes []string
	for rows.Next() {
		var prefix string
		if err := rows.Scan(&prefix); err != nil {
			rows.Close()
			return false, err
		}
		prefixes = append(prefixes, prefix)
	}
	err = rows.Err()
	err = errors.Join(err, rows.Close())
	if err != nil {
		return false, err
	}
	for _, prefix := range prefixes {
		if err := tx.operator.revokeAuthorityTx(ctx, tx, capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: prefix}); err != nil {
			return false, err
		}
	}
	return len(prefixes) > 0, tx.Commit()
}
