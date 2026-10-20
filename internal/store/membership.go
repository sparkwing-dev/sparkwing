package store

import (
	"context"
	"slices"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

// MembershipUpdate changes a member's role, or removes it when Role is nil.
type MembershipUpdate struct {
	AccountID string
	Role      *Role
}

// UpdateMembership preserves the last owner and invalidates cached membership authority atomically.
func (t *Tenant) UpdateMembership(ctx context.Context, actor Actor, change MembershipUpdate) error {
	if change.AccountID == "" || (change.Role != nil && !change.Role.Valid()) {
		return ErrInvalidInput
	}
	return t.s.inTx(ctx, func(tx *storeTx) error {
		if err := t.lockTeamTx(ctx, tx); err != nil {
			return err
		}
		p, err := resolveActorTx(ctx, tx, actor)
		if err != nil {
			return err
		}
		if p.Capability.Team != string(t.team) || p.Capability.Binding.Kind != capability.BindingNone || p.Capability.Subject.Kind != capability.SubjectAccount {
			return authorityError(apierr.Forbidden, "membership changes require an account")
		}
		selfRemoval := change.Role == nil && p.Capability.Subject.ID == change.AccountID
		if !selfRemoval && !slices.Contains(p.Capability.Grants, capability.TeamAdmin) {
			return authorityError(apierr.Forbidden, "membership changes require team administration")
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
		if change.Role == nil {
			_, err = tx.ExecContext(ctx, "DELETE FROM memberships WHERE team = ? AND account_id = ?", string(t.team), change.AccountID)
		} else {
			_, err = tx.ExecContext(ctx, "UPDATE memberships SET role = ? WHERE team = ? AND account_id = ?", string(*change.Role), string(t.team), change.AccountID)
		}
		if err != nil {
			return err
		}
		var keep Role
		if change.Role == nil {
			if err := t.moveSessionsOffTeamTx(ctx, tx, change.AccountID, tx.now()); err != nil {
				return err
			}
		} else {
			keep = *change.Role
		}
		if err := t.withdrawInvitationsIssuedByTx(ctx, tx, change.AccountID, keep, tx.now()); err != nil {
			return err
		}
		if change.Role == nil || keep == RoleReader {
			if err := t.revokeCapabilitiesMintedByTx(ctx, tx, change.AccountID); err != nil {
				return err
			}
		}
		if err := advanceAuthGenerationsTx(ctx, tx, nil, &t.team); err != nil {
			return err
		}
		return nil
	})
}

// safety: accepting an invitation grants the role it names without asking
// about its inviter again, so an invitation for more than the inviter now
// holds goes with that authority; an empty keep withdraws every one.
func (t *Tenant) withdrawInvitationsIssuedByTx(ctx context.Context, tx *storeTx, accountID string, keep Role, now time.Time) error {
	var above []any
	for _, r := range []Role{RoleReader, RoleEditor, RoleOwner} {
		if r.rank() > keep.rank() {
			above = append(above, string(r))
		}
	}
	if accountID == "" || len(above) == 0 {
		return nil
	}
	_, err := tx.ExecContext(ctx, `
		UPDATE invitations SET withdrawn_at = ?
		WHERE team = ? AND invited_by = ? AND accepted_at IS NULL AND withdrawn_at IS NULL
		  AND role IN (`+placeholders(len(above))+`)`,
		append([]any{now.UTC().UnixNano(), string(t.team), accountID}, above...)...)
	return err
}

func (t *Tenant) revokeCapabilitiesMintedByTx(ctx context.Context, tx *storeTx, accountID string) error {
	rows, err := tx.QueryContext(ctx, `SELECT prefix FROM capabilities WHERE team=? AND created_by=? AND (revoked_at IS NULL OR revoked_at>?) ORDER BY prefix`, t.team, accountID, tx.now().UnixNano())
	if err != nil {
		return err
	}
	prefixes, err := scanPrefixes(rows)
	if err != nil {
		return err
	}
	for _, prefix := range prefixes {
		if err := revokeAuthorityTx(ctx, tx, capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: prefix}); err != nil {
			return err
		}
	}
	return nil
}
