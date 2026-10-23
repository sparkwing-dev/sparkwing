package store

import (
	"context"
	"slices"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
	"github.com/sparkwing-dev/sparkwing/pkg/apierr"
)

func checkTeamAdministrationTx(ctx context.Context, tx *storeTx, team Team, actor runstate.Actor) (capability.Principal, error) {
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
func (t *Tenant) DeleteRunner(ctx context.Context, actor runstate.Actor, name string) (bool, error) {
	if name == "" {
		return false, authorityError(apierr.BadRequest, "runner name is required")
	}
	var found bool
	err := t.s.inTx(ctx, func(tx *storeTx) error {
		var prefixes []string
		if err := acquireLockSet(ctx, tx, func() lockSet {
			rows, err := tx.QueryContext(ctx, `SELECT prefix FROM capabilities WHERE team=? AND subject_kind='machine' AND subject_id=? AND bind_kind='none' AND parent_id IS NULL ORDER BY prefix`, t.team, name)
			if err != nil {
				return lockSet{err: err}
			}
			prefixes, err = scanPrefixes(rows)
			if err != nil {
				return lockSet{err: err}
			}
			leaves := []capability.AuthorityRef{actor.Authority}
			for _, prefix := range prefixes {
				leaves = append(leaves, capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: prefix})
			}
			set := discoverAuthorityLocks(ctx, tx, leaves, prefixes...)
			set.add(lockTeam, true, string(t.team))
			return set
		}); err != nil {
			return err
		}
		if _, err := checkTeamAdministrationTx(ctx, tx, t.team, actor); err != nil {
			return err
		}
		for _, prefix := range prefixes {
			if err := tx.operator.revokeAuthorityTx(ctx, tx, capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: prefix}); err != nil {
				return err
			}
		}
		found = len(prefixes) > 0
		return nil
	})
	return found, err
}
