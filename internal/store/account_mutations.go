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
	teams := append(slices.Clone(target), Team(a.Principal.Capability.Team))
	slices.Sort(teams)
	for _, team := range slices.Compact(teams) {
		if team == "" {
			continue
		}
		tenant := Tenant{s: tx.store, team: team}
		if err := tenant.lockTeamTx(ctx, tx); err != nil {
			return capability.Principal{}, err
		}
	}
	if err := lockAccountTx(ctx, tx, a.Principal.Capability.Subject.ID); err != nil {
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
	result, err := tx.ExecContext(ctx, `UPDATE sessions SET team=?,membership_epoch=COALESCE((SELECT epoch FROM membership_epochs WHERE team=? AND account_id=?),0) WHERE hash=? AND account_id=? AND COALESCE(team,'')=? AND capability_prefix='' AND revoked_at IS NULL AND expires_at>?`, team, team, p.Capability.Subject.ID, p.Authority.ID, p.Capability.Subject.ID, p.Capability.Team, tx.now().UnixNano())
	if err := credentialRowChanged(result, err); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE accounts SET active_team=?,updated_at=? WHERE id=?`, team, tx.now().UnixNano(), p.Capability.Subject.ID)
	return err
}

// SelectAccountTeam commits the account and direct session's current team together.
func (s *Store) SelectAccountTeam(ctx context.Context, a capability.Authorized, team Team) error {
	return s.inTx(ctx, func(tx *storeTx) error {
		p, err := accountSessionTx(ctx, tx, a, team)
		if err != nil {
			return err
		}
		return selectAccountTeamTx(ctx, tx, p, team)
	})
}

// CreateAccountTeam commits a new owner's team and session selection under live account authority.
func (s *Store) CreateAccountTeam(ctx context.Context, a capability.Authorized, input TeamInfo) (TeamInfo, error) {
	var out TeamInfo
	err := s.inTx(ctx, func(tx *storeTx) error {
		p, err := accountSessionTx(ctx, tx, a)
		if err != nil {
			return err
		}
		if input.CreatedBy != "" && input.CreatedBy != p.Capability.Subject.ID {
			return bindingRefusal()
		}
		out, err = createAccountTeamTx(ctx, tx, p.Capability.Subject.ID, input.Slug, input.DisplayName, tx.now())
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
func (s *Store) AcceptAccountInvitation(ctx context.Context, a capability.Authorized, id string) (InvitationAcceptance, error) {
	var out InvitationAcceptance
	err := s.inTx(ctx, func(tx *storeTx) error {
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
		out, err = acceptAccountInvitationTx(ctx, tx, p.Capability.Subject.ID, id, tx.now())
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
func (s *Store) UnlinkAccountIdentity(ctx context.Context, a capability.Authorized, provider string) (int64, error) {
	var ended int64
	err := s.inTx(ctx, func(tx *storeTx) error {
		p, err := accountSessionTx(ctx, tx, a)
		if err != nil {
			return err
		}
		if tx.now().Before(p.Capability.IssuedAt) || tx.now().Sub(p.Capability.IssuedAt) > IdentityChangeSignInWindow {
			return apierr.Authorization(apierr.ReasonReauthRequired, "sign in again before changing sign-in methods")
		}
		ended, err = unlinkAccountIdentityTx(ctx, tx, p.Capability.Subject.ID, provider, p.Authority.ID, tx.now())
		return err
	})
	if err != nil {
		return 0, err
	}
	return ended, nil
}
