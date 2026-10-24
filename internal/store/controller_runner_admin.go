package store

import (
	"context"
	"slices"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
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

// RunnerRevocation identifies the machine whose roots were revoked by a committed transaction.
type RunnerRevocation struct {
	MachineID     string
	TokenPrefixes []string
}

// DeleteRunner revokes every root registered under this tenant's machine identity.
func (t *Tenant) DeleteRunner(ctx context.Context, actor runstate.Actor, name string) (*RunnerRevocation, error) {
	if name == "" {
		return nil, authorityError(apierr.BadRequest, "runner name is required")
	}
	var revoked *RunnerRevocation
	err := t.s.inTx(ctx, func(tx *storeTx) error {
		revoked = nil
		var prefixes []string
		var machineID string
		if err := acquireLockSet(ctx, tx, func() lockSet {
			prefixes = nil
			rows, err := tx.queryContext(ctx, `SELECT prefix,subject_id FROM capabilities WHERE team=? AND subject_kind='machine' AND subject_id=? AND bind_kind='none' AND parent_id IS NULL ORDER BY prefix`, t.team, name)
			if err != nil {
				return lockSet{err: err}
			}
			defer rows.Close()
			for rows.Next() {
				var prefix string
				if err := rows.Scan(&prefix, &machineID); err != nil {
					return lockSet{err: err}
				}
				prefixes = append(prefixes, prefix)
			}
			if err := rows.Err(); err != nil {
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
		if len(prefixes) > 0 {
			revoked = &RunnerRevocation{MachineID: machineID, TokenPrefixes: prefixes}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return revoked, nil
}
