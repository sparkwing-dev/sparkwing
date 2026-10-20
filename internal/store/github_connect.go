package store

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

func (t *Tenant) githubConnectTx(ctx context.Context, a capability.Authorized, use func(*storeTx, capability.Principal, string) error, installations ...int64) error {
	return t.s.inTx(ctx, func(tx *storeTx) error {
		if err := acquireLockSet(ctx, tx, func() lockSet {
			set := discoverAuthorityLocks(ctx, tx, []capability.AuthorityRef{a.Principal.Authority})
			set.add(lockTeam, true, string(t.team))
			set.add(lockAccount, true, a.Principal.Capability.Subject.ID)
			for _, installation := range installations {
				set = discoverInstallationLocks(ctx, tx, set, installation)
			}
			return set
		}); err != nil {
			return err
		}
		return t.teamMutationInTx(ctx, tx, a, func(tx *storeTx, p capability.Principal) error {
			if !a.Principal.DirectAccountSession() || !p.DirectAccountSession() || p.Capability.Subject != a.Principal.Capability.Subject || p.Capability.Team != a.Principal.Capability.Team || !p.Capability.IssuedAt.Equal(a.Principal.Capability.IssuedAt) {
				return bindingRefusal()
			}
			var subject string
			err := tx.QueryRowContext(ctx, `SELECT subject FROM identities WHERE account_id=? AND provider=?`, p.Capability.Subject.ID, ProviderGitHub).Scan(&subject)
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNotFound
			}
			if err != nil {
				return err
			}
			return use(tx, p, subject)
		})
	})
}

// LinkedGitHubSubject reads the current direct session account's GitHub identity.
func (t *Tenant) LinkedGitHubSubject(ctx context.Context, a capability.Authorized) (string, error) {
	var out string
	err := t.githubConnectTx(ctx, a, func(_ *storeTx, _ capability.Principal, subject string) error {
		out = subject
		return nil
	})
	if err != nil {
		return "", err
	}
	return out, nil
}

// ConsumeGitHubConnect commits a live administrator's unexpired connection nonce once.
func (t *Tenant) ConsumeGitHubConnect(ctx context.Context, a capability.Authorized, nonce string, expires time.Time) error {
	return t.githubConnectTx(ctx, a, func(tx *storeTx, _ capability.Principal, _ string) error {
		used, err := consumeFlowNonceTx(ctx, tx, "github_app_connect", nonce, expires)
		if err != nil {
			return err
		}
		if !used {
			return bindingRefusal()
		}
		return nil
	})
}

// BindAuthorizedGitHubInstallation commits provider facts under the current linked identity.
func (t *Tenant) BindAuthorizedGitHubInstallation(ctx context.Context, a capability.Authorized, row GitHubAppInstallation) (GitHubAppInstallation, error) {
	var out GitHubAppInstallation
	err := t.githubConnectTx(ctx, a, func(tx *storeTx, p capability.Principal, subject string) error {
		if row.Team != "" && row.Team != t.team || row.ConnectedBy != "" && row.ConnectedBy != p.Capability.Subject.ID || row.GitHubUserID <= 0 || strconv.FormatInt(row.GitHubUserID, 10) != subject || row.AccountType != "User" && row.AccountType != "Organization" {
			return bindingRefusal()
		}
		var previousTeam Team
		if row.Reclaim {
			previous, err := githubInstallationOwnerTx(ctx, tx, row.InstallationID)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return err
			}
			if err == nil && previous != string(t.team) {
				previousTeam = Team(previous)
				prior := &Tenant{s: t.s, team: Team(previous)}
				if err := prior.unbindGitHubAppInstallationTx(ctx, tx, row.InstallationID); err != nil {
					return err
				}
				if _, err := tx.ExecContext(ctx, `DELETE FROM github_app_automation WHERE team=? AND installation_id=?`, previous, row.InstallationID); err != nil {
					return err
				}
			}
		}
		row.ConnectedBy = p.Capability.Subject.ID
		var err error
		out, err = t.bindGitHubInstallationTx(ctx, tx, row, tx.now())
		if err != nil {
			return err
		}
		if previousTeam != "" {
			transfer, err := newIdentityID()
			if err != nil {
				return err
			}
			for _, team := range []Team{previousTeam, t.team} {
				if err := RecordBusinessEvent(tx, BusinessEvent{At: tx.now(), Team: team, Kind: "github.installation_reclaimed", SubjectID: transfer, Actor: p.Capability.Subject.ID, Attrs: map[string]any{"installation_id": row.InstallationID, "previous_team": previousTeam, "new_team": t.team, "github_user_id": row.GitHubUserID}}); err != nil {
					return err
				}
			}
			out.ReclaimedFrom = previousTeam
		}
		return nil
	}, row.InstallationID)
	if err != nil {
		return GitHubAppInstallation{}, err
	}
	return out, nil
}

func githubInstallationOwnerTx(ctx context.Context, tx *storeTx, installation int64) (string, error) {
	var owner string
	err := tx.QueryRowContext(ctx, `SELECT team FROM github_app_installations WHERE installation_id=?`, installation).Scan(&owner)
	return owner, err
}
