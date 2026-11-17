package store

import (
	"context"
	"slices"

	hosterr "github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/sparkwing/protocol/apierr"
)

// AccountDeletionPreview lists the teams deleted with the account and those blocking deletion.
type AccountDeletionPreview struct {
	AccountID      string
	SignInRequired bool
	Teams          []Team
	BlockedTeams   []TeamInfo
}

func ownAccountDeletionSessionTx(ctx context.Context, tx *storeTx, a capability.Authorized) (capability.Principal, error) {
	if err := acquireLockSet(ctx, tx, func() lockSet {
		set := discoverAuthorityLocks(ctx, tx, []capability.AuthorityRef{a.Principal.Authority}, a.Principal.Authority.ID)
		set = tx.operator.discoverAccountSessionLocks(ctx, tx, set, a.Principal.Capability.Subject.ID)
		return tx.operator.discoverAccountDeletionLocks(ctx, tx, set, a.Principal.Capability.Subject.ID)
	}); err != nil {
		return capability.Principal{}, err
	}
	return accountSessionTx(ctx, tx, a)
}

// PreviewAccountDeletion reads the deletion scope under live direct account authority.
func (o *Operator) PreviewAccountDeletion(ctx context.Context, a capability.Authorized) (AccountDeletionPreview, error) {
	var result AccountDeletionPreview
	err := o.s.inTx(ctx, func(tx *storeTx) error {
		p, err := ownAccountDeletionSessionTx(ctx, tx, a)
		if err != nil {
			return err
		}
		sole, blocked, err := tx.operator.ownedTeamsTx(ctx, tx, p.Capability.Subject.ID)
		now := tx.now(ctx)
		result = AccountDeletionPreview{AccountID: p.Capability.Subject.ID, Teams: sole, BlockedTeams: blocked, SignInRequired: now.Before(p.Capability.IssuedAt) || now.Sub(p.Capability.IssuedAt) > IdentityChangeSignInWindow}
		return err
	})
	if err != nil {
		return AccountDeletionPreview{}, err
	}
	return result, nil
}

// DeleteOwnAccount compares the confirmed scope and commits deletion and its audit fact together.
func (o *Operator) DeleteOwnAccount(ctx context.Context, a capability.Authorized, confirm string, teams []string) (AccountDeletion, error) {
	var result AccountDeletion
	err := o.s.inTx(ctx, func(tx *storeTx) error {
		p, err := ownAccountDeletionSessionTx(ctx, tx, a)
		if err != nil {
			return err
		}
		now := tx.now(ctx)
		if now.Before(p.Capability.IssuedAt) || now.Sub(p.Capability.IssuedAt) > IdentityChangeSignInWindow {
			return hosterr.Authorization(hosterr.ReasonReauthRequired, "sign out and sign in again before deleting your account, then retry within 10 minutes with the new session")
		}
		if confirm == "" || confirm != p.Capability.Subject.ID {
			return &apierr.Error{Code: apierr.BadRequest, Message: "confirm_account_id must match your account ID"}
		}
		sole, blocked, err := tx.operator.ownedTeamsTx(ctx, tx, p.Capability.Subject.ID)
		if err != nil {
			return err
		}
		if len(blocked) > 0 {
			return &LastOwnerError{Teams: blocked}
		}
		if len(sole) > 0 && len(teams) == 0 {
			return &apierr.Error{Code: apierr.BadRequest, Message: "confirm_teams is required: confirm every team listed in the deletion preview"}
		}
		slices.Sort(sole)
		confirmed := slices.Clone(teams)
		slices.Sort(confirmed)
		if len(confirmed) != len(sole) {
			return &apierr.Error{Code: apierr.Conflict, Message: "teams to delete changed; preview account deletion and confirm every listed team again"}
		}
		for i, team := range sole {
			if confirmed[i] != string(team) {
				return &apierr.Error{Code: apierr.Conflict, Message: "teams to delete changed; preview account deletion and confirm every listed team again"}
			}
		}
		if err := tx.operator.recordAudit(ctx, tx, auditContext{At: tx.now(ctx), Actor: auditPrincipalActor(p)}, auditAccountDeletion{AccountID: confirm}); err != nil {
			return err
		}
		result, err = tx.operator.deleteAccountTx(ctx, tx, confirm, tx.now(ctx))
		return err
	})
	if err != nil {
		return AccountDeletion{}, err
	}
	return result, nil
}
