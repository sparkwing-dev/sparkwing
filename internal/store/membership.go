package store

import (
	"context"
	"slices"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
)

// MembershipUpdate changes a member's role, or removes it when Role is nil.
type MembershipUpdate struct {
	AccountID string
	Role      *Role
}

// UpdateMembership preserves the last owner and fences removed or demoted membership authority atomically.
func (t *Tenant) UpdateMembership(ctx context.Context, actor runstate.Actor, change MembershipUpdate) error {
	if change.AccountID == "" || (change.Role != nil && !change.Role.Valid()) {
		return ErrInvalidInput
	}
	return t.s.inTx(ctx, func(tx *storeTx) error {
		if err := acquireLockSet(ctx, tx, func() lockSet {
			set := discoverAuthorityLocks(ctx, tx, []capability.AuthorityRef{actor.Authority})
			set.add(lockTeam, true, string(t.team))
			set.add(lockAccount, true, change.AccountID)
			set.rows = append(set.rows, rowLock{key: storeLockKey{tier: lockMembership, keys: [3]string{string(t.team), change.AccountID}}, write: true, optional: true})
			if change.Role == nil {
				set = tx.operator.discoverAccountSessionLocks(ctx, tx, set, change.AccountID)
			}
			return set
		}); err != nil {
			return err
		}
		p, err := resolveTeamActorTx(ctx, tx, t.team, actor, sameTeam)
		if err != nil {
			return err
		}
		if p.Capability.Binding.Kind != capability.BindingNone || p.Capability.Subject.Kind != capability.SubjectAccount {
			return authorityError(apierr.Forbidden, "membership changes require an account")
		}
		// safety: leaving is the member's own act, so a token they hold cannot do it for them.
		selfRemoval := change.Role == nil && p.Capability.Subject.ID == change.AccountID && p.DirectAccountSession()
		if !selfRemoval && !slices.Contains(p.Capability.Grants, capability.TeamAdmin) {
			return authorityError(apierr.Forbidden, "membership changes require team administration; to leave a team, sign in and leave from the console")
		}
		actorRole, err := t.roleTx(ctx, tx, p.Capability.Subject.ID)
		if err != nil {
			return err
		}
		current, err := t.roleTx(ctx, tx, change.AccountID)
		if err != nil {
			return err
		}
		if !actorRole.AtLeast(current) || (change.Role != nil && !actorRole.AtLeast(*change.Role)) {
			return ErrRoleAboveOwn
		}
		if change.Role != nil && current == *change.Role {
			return nil
		}
		if current == RoleOwner {
			if err := t.requireAnotherOwnerTx(ctx, tx, change.AccountID); err != nil {
				return err
			}
		}
		if change.Role == nil || !change.Role.AtLeast(current) {
			if _, err := tx.execContext(ctx, `INSERT INTO membership_epochs(team,account_id,epoch) VALUES (?,?,1) ON CONFLICT(team,account_id) DO UPDATE SET epoch=membership_epochs.epoch+1`, t.team, change.AccountID); err != nil {
				return err
			}
		}
		if change.Role == nil {
			_, err = tx.execContext(ctx, "DELETE FROM memberships WHERE team = ? AND account_id = ?", string(t.team), change.AccountID)
		} else {
			_, err = tx.execContext(ctx, "UPDATE memberships SET role = ? WHERE team = ? AND account_id = ?", string(*change.Role), string(t.team), change.AccountID)
		}
		if err != nil {
			return err
		}
		if change.Role == nil {
			if err := t.moveSessionsOffTeamTx(ctx, tx, change.AccountID, tx.now(ctx)); err != nil {
				return err
			}
		}
		operation := "membership.removed"
		if change.Role != nil {
			operation = "membership.role:" + string(*change.Role)
		}
		return recordMutationAuditTx(ctx, tx, t.team, auditPrincipalActor(p), operation, change.AccountID)
	})
}
