package store

import (
	"context"
	"database/sql"
	"errors"
	"slices"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

func accountSessionTx(ctx context.Context, tx *storeTx, a capability.Authorized, target ...Team) (capability.Principal, error) {
	if !a.Principal.DirectAccountSession() || a.Access.Credential != capability.CredentialAccount || a.Access.Binding != capability.BindNone || a.Access.Liveness != capability.LiveCredential || a.ResultReplay || a.LogTailAttempt != 0 || a.Target.RunID != "" || a.Target.NodeID != "" || a.Target.AccountID != "" && a.Target.AccountID != a.Principal.Capability.Subject.ID {
		return capability.Principal{}, bindingRefusal()
	}
	if err := acquireLockSet(ctx, tx, func() lockSet {
		set := discoverAuthorityLocks(ctx, tx, []capability.AuthorityRef{a.Principal.Authority}, a.Principal.Authority.ID)
		set.add(lockAccount, true, a.Principal.Capability.Subject.ID)
		for _, team := range append(slices.Clone(target), Team(a.Principal.Capability.Team)) {
			if team != "" {
				set.add(lockTeam, true, string(team))
			}
		}
		return tx.operator.discoverAccountSessionLocks(ctx, tx, set, a.Principal.Capability.Subject.ID)
	}); err != nil {
		return capability.Principal{}, err
	}
	p, err := tx.ResolveAuthority(ctx, a.Principal.Authority)
	if err != nil {
		return p, err
	}
	c := p.Capability
	if !p.DirectAccountSession() || c.Subject != a.Principal.Capability.Subject || c.Team != a.Principal.Capability.Team || !c.IssuedAt.Equal(a.Principal.Capability.IssuedAt) {
		return p, bindingRefusal()
	}
	return p, nil
}

func selectAccountTeamTx(ctx context.Context, tx *storeTx, p capability.Principal, team Team) error {
	if team == "" || NormalizeTeam(team) != team {
		return ErrInvalidInput
	}
	t := Tenant{s: tx.store, team: team}
	if _, err := t.roleTx(ctx, tx, p.Capability.Subject.ID); err != nil {
		return err
	}
	if err := authenticateSessionTeamTx(ctx, tx, p.Authority.ID, team, p.Capability.Subject.ID); err != nil {
		return err
	}
	clockTime1, clockErr1 := tx.now(ctx)
	if clockErr1 != nil {
		return clockErr1
	}
	result, err := tx.ExecContext(ctx, `UPDATE sessions SET team=? WHERE hash=? AND account_id=? AND COALESCE(team,'')=? AND capability_prefix='' AND revoked_at IS NULL AND expires_at>?`, team, p.Authority.ID, p.Capability.Subject.ID, p.Capability.Team, clockTime1.UnixNano())
	if err := credentialRowChanged(result, err); err != nil {
		return err
	}
	clockTime2, clockErr2 := tx.now(ctx)
	if clockErr2 != nil {
		return clockErr2
	}
	_, err = tx.ExecContext(ctx, `UPDATE accounts SET active_team=?,updated_at=? WHERE id=?`, team, clockTime2.UnixNano(), p.Capability.Subject.ID)
	return err
}

// SelectAccountTeam commits the account and direct session's current team together.
func (o *Operator) SelectAccountTeam(ctx context.Context, a capability.Authorized, team Team) error {
	return o.s.inTx(ctx, func(tx *storeTx) error {
		p, err := accountSessionTx(ctx, tx, a, team)
		if err != nil {
			return err
		}
		return selectAccountTeamTx(ctx, tx, p, team)
	})
}

// CreateAccountTeam commits a new owner's team and session selection under live account authority.
func (o *Operator) CreateAccountTeam(ctx context.Context, a capability.Authorized, input TeamInfo) (TeamInfo, error) {
	var out TeamInfo
	err := o.s.inTx(ctx, func(tx *storeTx) error {
		p, err := accountSessionTx(ctx, tx, a)
		if err != nil {
			return err
		}
		if input.CreatedBy != "" && input.CreatedBy != p.Capability.Subject.ID {
			return bindingRefusal()
		}
		clockTime3, clockErr3 := tx.now(ctx)
		if clockErr3 != nil {
			return clockErr3
		}
		out, err = createAccountTeamTx(ctx, tx, p.Capability.Subject.ID, input.Slug, input.DisplayName, clockTime3)
		if err != nil {
			return err
		}
		return selectAccountTeamTx(ctx, tx, p, out.Slug)
	})
	if err != nil {
		return TeamInfo{}, err
	}
	return out, nil
}

// AcceptAccountInvitation commits verified invitation membership and session selection together.
func (o *Operator) AcceptAccountInvitation(ctx context.Context, a capability.Authorized, id string) (InvitationAcceptance, error) {
	var out InvitationAcceptance
	err := o.s.inTx(ctx, func(tx *storeTx) error {
		var team Team
		lookupErr := tx.QueryRowContext(ctx, `SELECT team FROM invitations WHERE id=?`, id).Scan(&team)
		p, err := accountSessionTx(ctx, tx, a, team)
		if err != nil {
			return err
		}
		if errors.Is(lookupErr, sql.ErrNoRows) {
			return ErrNotFound
		}
		if lookupErr != nil {
			return lookupErr
		}
		out, err = tx.operator.acceptAccountInvitationTx(ctx, tx, p.Capability.Subject.ID, id)
		if err != nil {
			return err
		}
		return selectAccountTeamTx(ctx, tx, p, out.Team)
	})
	if err != nil {
		return InvitationAcceptance{}, err
	}
	return out, nil
}

// UnlinkAccountIdentity retains the selected recent direct session and the last sign-in method.
func (o *Operator) UnlinkAccountIdentity(ctx context.Context, a capability.Authorized, provider string) (int64, error) {
	var ended int64
	err := o.s.inTx(ctx, func(tx *storeTx) error {
		p, err := accountSessionTx(ctx, tx, a)
		if err != nil {
			return err
		}
		clockTime5, clockErr5 := tx.now(ctx)
		if clockErr5 != nil {
			return clockErr5
		}
		if clockTime5.Before(p.Capability.IssuedAt) || clockTime5.Sub(p.Capability.IssuedAt) > IdentityChangeSignInWindow {
			return apierr.Authorization(apierr.ReasonReauthRequired, "sign in again before changing sign-in methods")
		}
		clockTime6, clockErr6 := tx.now(ctx)
		if clockErr6 != nil {
			return clockErr6
		}
		ended, err = tx.operator.unlinkAccountIdentityTx(ctx, tx, p.Capability.Subject.ID, provider, p.Authority.ID, clockTime6)
		return err
	})
	if err != nil {
		return 0, err
	}
	return ended, nil
}
